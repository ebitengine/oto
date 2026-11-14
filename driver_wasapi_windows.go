// Copyright 2022 The Oto Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package oto

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ebitengine/oto/v3/internal/mathutil"
	"github.com/ebitengine/oto/v3/internal/mux"
)

type comThread struct {
	funcCh chan func()
	done   chan struct{}

	stopped bool
	m       sync.Mutex
}

func newCOMThread() (*comThread, error) {
	funcCh := make(chan func())
	errCh := make(chan error)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// S_FALSE is returned when CoInitializeEx is nested. This is a successful case.
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(windows.S_FALSE)) {
			errCh <- err
			return
		}
		// CoUninitialize should be called even when CoInitializeEx returns S_FALSE.
		defer windows.CoUninitialize()

		close(errCh)

		for f := range funcCh {
			f()
		}
	}()

	if err := <-errCh; err != nil {
		return nil, err
	}

	return &comThread{
		funcCh: funcCh,
		done:   done,
	}, nil
}

// Run calls f on the COM thread and waits for it. f is not called after Stop.
func (c *comThread) Run(f func()) {
	c.m.Lock()
	defer c.m.Unlock()

	if c.stopped {
		return
	}

	ch := make(chan struct{})
	c.funcCh <- func() {
		f()
		close(ch)
	}
	<-ch
}

// Stop terminates the COM thread. Stop must not be called on the COM thread.
func (c *comThread) Stop() {
	c.m.Lock()
	defer c.m.Unlock()

	if c.stopped {
		return
	}
	c.stopped = true
	close(c.funcCh)
	<-c.done
}

type wasapiContext struct {
	sampleRate        int
	channelCount      int
	mux               *mux.Mux
	bufferSizeInBytes int

	comThread     *comThread
	err           atomicError
	suspended     bool
	suspendedCond *sync.Cond

	sampleReadyEvent windows.Handle
	client           *_IAudioClient2
	bufferFrames     uint32
	renderClient     *_IAudioRenderClient
	audioClock       *_IAudioClock
	clockFrequency   uint64
	currentDeviceID  string
	enumerator       *_IMMDeviceEnumerator
	oomRetryCount    int

	buf []float32

	// mixed counts the frames of the mix read so far, across every stream.
	mixed int64

	// streamStart is the frame of the mix at the stream's position 0.
	streamStart int64

	// filledAt is when the buffer was last filled.
	filledAt time.Time

	m sync.Mutex
}

var (
	errDeviceSwitched = errors.New("oto: device switched")
)

const (
	wasapiOOMRetryLimit = 3

	// wasapiRestartRetryLimit caps the transient-error re-acquisition attempts
	// before the error is surfaced. With the backoff intervals below, this is
	// roughly ten seconds of continuous outage.
	wasapiRestartRetryLimit = 24

	// wasapiReacquireMinInterval and wasapiReacquireMaxInterval bound the backoff
	// between device re-acquisition attempts, each a full COM round trip. Mux
	// draining continues at real time regardless.
	wasapiReacquireMinInterval = 50 * time.Millisecond
	wasapiReacquireMaxInterval = 500 * time.Millisecond
)

func newWASAPIContext(sampleRate, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (context *wasapiContext, ferr error) {
	t, err := newCOMThread()
	if err != nil {
		return nil, err
	}

	c := &wasapiContext{
		sampleRate:        sampleRate,
		channelCount:      channelCount,
		mux:               mux,
		bufferSizeInBytes: bufferSizeInBytes,
		comThread:         t,
		suspendedCond:     sync.NewCond(&sync.Mutex{}),
	}

	defer func() {
		if ferr != nil {
			c.close()
		}
	}()

	ev, err := windows.CreateEventEx(nil, nil, 0, windows.EVENT_ALL_ACCESS)
	if err != nil {
		return nil, err
	}
	c.sampleReadyEvent = ev

	if err := c.start(); err != nil {
		return nil, err
	}

	return c, nil
}

func (c *wasapiContext) isDeviceSwitched() (bool, error) {
	// If the audio is suspended, do nothing.
	if c.isSuspended() {
		return false, nil
	}

	var switched bool
	var cerr error
	c.comThread.Run(func() {
		device, err := c.enumerator.GetDefaultAudioEndpoint(eRender, eConsole)
		if err != nil {
			if errors.Is(err, _E_NOTFOUND) {
				// No default endpoint can be temporary while devices are switching.
				cerr = errDeviceSwitched
				return
			}
			cerr = err
			return
		}
		defer device.Release()

		id, err := device.GetId()
		if err != nil {
			cerr = err
			return
		}

		if c.currentDeviceID == id {
			return
		}
		switched = true
	})

	return switched, cerr
}

func (c *wasapiContext) start() error {
	if err := c.initialize(); err != nil {
		return err
	}
	go c.run()
	return nil
}

func (c *wasapiContext) initialize() error {
	var cerr error
	c.comThread.Run(func() {
		cerr = c.startOnCOMThread()
	})
	return cerr
}

func (c *wasapiContext) run() {
	// This goroutine owns the resources across all recovery attempts. The render
	// loop has returned before cleanup can close the event or release interfaces.
	defer c.close()

	for {
		err := c.loop()
		c.comThread.Run(c.releaseOnCOMThread)
		if err == nil {
			return
		}
		// E_OUTOFMEMORY from IAudioRenderClient::GetBuffer has been observed on Xbox.
		// The counter is reset after a successful buffer write, so this cap applies
		// to consecutive failures without intervening progress.
		if errors.Is(err, _E_OUTOFMEMORY) {
			if c.oomRetryCount >= wasapiOOMRetryLimit {
				c.err.Join(err)
				return
			}
			c.oomRetryCount++
		} else if !errors.Is(err, _AUDCLNT_E_DEVICE_INVALIDATED) && !errors.Is(err, _AUDCLNT_E_RESOURCES_INVALIDATED) && !errors.Is(err, errDeviceSwitched) && !errors.Is(err, _RPC_E_DISCONNECTED) {
			c.err.Join(err)
			return
		}

		if err := c.restart(); err != nil {
			c.err.Join(err)
			return
		}
	}
}

// close must be called outside the COM thread after rendering has ended.
func (c *wasapiContext) close() {
	c.comThread.Run(c.releaseOnCOMThread)
	if c.sampleReadyEvent != 0 {
		windows.CloseHandle(c.sampleReadyEvent)
		c.sampleReadyEvent = 0
	}
	c.comThread.Stop()
}

func (c *wasapiContext) releaseOnCOMThread() {
	if c.client != nil {
		_, _ = c.client.Stop()
	}
	// The report holds only for the stream released here.
	c.mux.ClearPlayed()
	// The render service must be released on the thread that acquired it,
	// before releasing the audio client that owns it.
	if c.audioClock != nil {
		c.audioClock.Release()
		c.audioClock = nil
	}
	if c.renderClient != nil {
		c.renderClient.Release()
		c.renderClient = nil
	}
	if c.client != nil {
		c.client.Release()
		c.client = nil
	}
	if c.enumerator != nil {
		c.enumerator.Release()
		c.enumerator = nil
	}
}

func (c *wasapiContext) startOnCOMThread() (ferr error) {
	c.releaseOnCOMThread()
	defer func() {
		if ferr != nil {
			c.releaseOnCOMThread()
		}
	}()

	// Recreate the enumerator because it can become disconnected across
	// suspend/resume or an audio service restart.
	e, err := _CoCreateInstance(&uuidMMDeviceEnumerator, nil, uint32(_CLSCTX_ALL), &uuidIMMDeviceEnumerator)
	if err != nil {
		return err
	}
	c.enumerator = (*_IMMDeviceEnumerator)(e)

	device, err := c.enumerator.GetDefaultAudioEndpoint(eRender, eConsole)
	if err != nil {
		if errors.Is(err, _E_NOTFOUND) {
			return errDeviceNotFound
		}
		return err
	}
	defer device.Release()

	id, err := device.GetId()
	if err != nil {
		return err
	}
	c.currentDeviceID = id

	client, err := device.Activate(&uuidIAudioClient2, uint32(_CLSCTX_ALL), nil)
	if err != nil {
		return err
	}
	c.client = (*_IAudioClient2)(client)

	if err := c.client.SetClientProperties(&_AudioClientProperties{
		cbSize:     uint32(unsafe.Sizeof(_AudioClientProperties{})),
		bIsOffload: 0,                    // false
		eCategory:  _AudioCategory_Other, // In the example, AudioCategory_ForegroundOnlyMedia was used, but this value is deprecated.
	}); err != nil {
		return err
	}

	// Check the format is supported by WASAPI.
	// Stereo with 48000 [Hz] is likely supported, but mono and/or other sample rates are unlikely to be supported.
	// Fallback to WinMM in this case anyway.
	const bitsPerSample = 32
	nBlockAlign := c.channelCount * bitsPerSample / 8
	var channelMask uint32
	switch c.channelCount {
	case 1:
		channelMask = _SPEAKER_FRONT_CENTER
	case 2:
		channelMask = _SPEAKER_FRONT_LEFT | _SPEAKER_FRONT_RIGHT
	}
	f := &_WAVEFORMATEXTENSIBLE{
		wFormatTag:      _WAVE_FORMAT_EXTENSIBLE,
		nChannels:       uint16(c.channelCount),
		nSamplesPerSec:  uint32(c.sampleRate),
		nAvgBytesPerSec: uint32(c.sampleRate * nBlockAlign),
		nBlockAlign:     uint16(nBlockAlign),
		wBitsPerSample:  bitsPerSample,
		cbSize:          0x16,
		Samples:         bitsPerSample,
		dwChannelMask:   channelMask,
		SubFormat:       _KSDATAFORMAT_SUBTYPE_IEEE_FLOAT,
	}

	var bufferSizeIn100ns _REFERENCE_TIME
	if c.bufferSizeInBytes != 0 {
		bufferSizeInFrames := int64(c.bufferSizeInBytes) / int64(nBlockAlign)
		duration, ok := mathutil.MulDiv(bufferSizeInFrames, 1e7, int64(c.sampleRate))
		if !ok {
			return fmt.Errorf("oto: WASAPI buffer duration is unrepresentable")
		}
		bufferSizeIn100ns = _REFERENCE_TIME(duration)
	} else {
		// The default buffer size can be too small and might cause glitch noises.
		// Specify 50[ms] as the buffer size.
		bufferSizeIn100ns = _REFERENCE_TIME(50 * time.Millisecond / 100)
	}

	// Even if the sample rate and/or the number of channels are not supported by the audio driver,
	// AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM should convert the sample rate automatically (#215).
	// AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY used together with
	// AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM tells WASAPI to use a high-quality
	// resampler. Without it, 44100 source played on a 48000 device sounds bad.
	if err := c.client.Initialize(_AUDCLNT_SHAREMODE_SHARED,
		_AUDCLNT_STREAMFLAGS_EVENTCALLBACK|_AUDCLNT_STREAMFLAGS_NOPERSIST|_AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM|_AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY,
		bufferSizeIn100ns, 0, f, nil); err != nil {
		return err
	}

	frames, err := c.client.GetBufferSize()
	if err != nil {
		return err
	}
	c.bufferFrames = frames

	renderClient, err := c.client.GetService(&uuidIAudioRenderClient)
	if err != nil {
		return err
	}
	c.renderClient = (*_IAudioRenderClient)(renderClient)

	if err := c.client.SetEventHandle(c.sampleReadyEvent); err != nil {
		return err
	}

	// The audio clock tells which frame of the stream is played. Without it,
	// the stream plays and nothing is reported.
	if audioClock, err := c.client.GetService(&uuidIAudioClock); err == nil {
		c.audioClock = (*_IAudioClock)(audioClock)
		freq, err := c.audioClock.GetFrequency()
		if err != nil || freq == 0 {
			c.audioClock.Release()
			c.audioClock = nil
		}
		c.clockFrequency = freq
	}

	// TODO: Should some errors be allowed? See WASAPIManager.cpp in the official example SimpleWASAPIPlaySound.

	// Fill the buffer before starting, so that the stream's position 0 is a
	// known frame of the mix.
	c.m.Lock()
	defer c.m.Unlock()
	if err := c.startFilled(); err != nil {
		return err
	}

	return nil
}

// startFilled fills the empty buffer and starts the stream from position 0.
// c.m must be held.
func (c *wasapiContext) startFilled() error {
	c.streamStart = c.mixed
	if err := c.fill(c.bufferFrames); err != nil {
		return err
	}
	return c.client.Start()
}

func (c *wasapiContext) loop() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// S_FALSE is returned when CoInitializeEx is nested. This is a successful case.
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(windows.S_FALSE)) {
		return err
	}
	// CoUninitialize should be called even when CoInitializeEx returns S_FALSE.
	defer windows.CoUninitialize()

	if err := c.loopOnRenderThread(); err != nil {
		return err
	}

	return nil
}

func (c *wasapiContext) loopOnRenderThread() error {
	last := time.Now()
	for {
		if err := c.waitUntilResumed(); err != nil {
			return err
		}

		// Keep checking suspension and device changes even if the endpoint stops
		// signaling its sample-ready event.
		evt, err := windows.WaitForSingleObject(c.sampleReadyEvent, 500)
		if err != nil {
			return err
		}
		if evt != windows.WAIT_OBJECT_0 && evt != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("oto: WaitForSingleObject failed: returned value: %d", evt)
		}

		if evt == windows.WAIT_OBJECT_0 {
			if err := c.writeOnRenderThread(); err != nil {
				return err
			}
		}

		// Checking the current default audio device might be an expensive operation.
		// Check this repeatedly but with some time interval.
		if now := time.Now(); now.Sub(last) >= 500*time.Millisecond {
			switched, err := c.isDeviceSwitched()
			if err != nil {
				return err
			}
			if switched {
				return errDeviceSwitched
			}
			last = now
		}
	}
}

// waitUntilResumed blocks while the context is suspended. The stream is
// stopped meanwhile, so that its position stays at the frame it plays.
func (c *wasapiContext) waitUntilResumed() error {
	c.suspendedCond.L.Lock()
	suspended := c.suspended
	c.suspendedCond.L.Unlock()
	if !suspended {
		return nil
	}

	if _, err := c.client.Stop(); err != nil {
		return err
	}
	c.suspendedCond.L.Lock()
	for c.suspended {
		c.suspendedCond.Wait()
	}
	c.suspendedCond.L.Unlock()
	return c.client.Start()
}

func (c *wasapiContext) writeOnRenderThread() error {
	c.m.Lock()
	defer c.m.Unlock()

	paddingFrames, err := c.client.GetCurrentPadding()
	if err != nil {
		return err
	}

	// An empty buffer a whole buffer's time after the last fill means the stream
	// ran dry. Its position kept counting the silence played meanwhile, so start
	// the stream again from position 0. An empty buffer sooner than that can be
	// the engine taking the last period just now, with no silence played yet.
	bufferTime := time.Duration(int64(c.bufferFrames) * int64(time.Second) / int64(c.sampleRate))
	if paddingFrames == 0 && c.audioClock != nil && time.Since(c.filledAt) >= bufferTime {
		c.mux.ClearPlayed()
		if _, err := c.client.Stop(); err != nil {
			return err
		}
		if err := c.client.Reset(); err != nil {
			return err
		}
		if err := c.startFilled(); err != nil {
			return err
		}
		c.oomRetryCount = 0
		return nil
	}

	// Defensive check: WASAPI should never report more padding than the buffer size,
	// but if it did, the uint32 subtraction below would wrap around to a huge frame
	// count and GetBuffer would fail, so skip the write instead.
	if paddingFrames >= c.bufferFrames {
		return nil
	}
	if err := c.fill(c.bufferFrames - paddingFrames); err != nil {
		return err
	}
	c.oomRetryCount = 0
	return c.reportPlayed()
}

// fill writes frames of the mix to the buffer. c.m must be held.
func (c *wasapiContext) fill(frames uint32) error {
	// Get the destination buffer.
	dstBuf, err := c.renderClient.GetBuffer(frames)
	if err != nil {
		return err
	}

	// Calculate the buffer size.
	if buflen := int(frames) * c.channelCount; cap(c.buf) < buflen {
		c.buf = make([]float32, buflen)
	} else {
		c.buf = c.buf[:buflen]
	}

	// Read the buffer from the players.
	c.mux.ReadFloat32s(c.buf)
	c.mixed += int64(frames)
	c.filledAt = time.Now()

	// Copy the read buf to the destination buffer.
	copy(unsafe.Slice((*float32)(unsafe.Pointer(dstBuf)), len(c.buf)), c.buf)

	// Release the buffer.
	if err := c.renderClient.ReleaseBuffer(frames, 0); err != nil {
		return err
	}

	c.buf = c.buf[:0]
	return nil
}

// reportPlayed reports to the mux which frame of the mix is played, from the
// stream's position.
func (c *wasapiContext) reportPlayed() error {
	if c.audioClock == nil {
		return nil
	}
	pos, qpc, err := c.audioClock.GetPosition()
	if err != nil {
		// A lost device restarts the stream. Other errors only skip this report.
		if isWASAPIDeviceTransientError(err) {
			return err
		}
		return nil
	}
	// The position stays 0 until the first frame plays.
	if pos == 0 {
		return nil
	}
	now, err := queryPerformanceCounter100ns()
	if err != nil {
		return nil
	}
	frames, ok := mathutil.MulDiv(int64(pos), int64(c.sampleRate), int64(c.clockFrequency))
	if !ok {
		return nil
	}
	at := time.Now().Add(time.Duration(int64(qpc)-now) * 100)

	c.suspendedCond.L.Lock()
	defer c.suspendedCond.L.Unlock()
	// Suspend clears the report, so a report made since is dropped.
	if !c.suspended {
		c.mux.SetPlayed(c.streamStart+frames, at)
	}
	return nil
}

func (c *wasapiContext) Suspend() error {
	c.suspendedCond.L.Lock()
	c.suspended = true
	c.mux.ClearPlayed()
	c.suspendedCond.L.Unlock()
	c.suspendedCond.Signal()

	return nil
}

func (c *wasapiContext) Resume() error {
	c.suspendedCond.L.Lock()
	c.suspended = false
	c.suspendedCond.L.Unlock()
	c.suspendedCond.Signal()

	return nil
}

func (c *wasapiContext) isSuspended() bool {
	c.suspendedCond.L.Lock()
	defer c.suspendedCond.L.Unlock()
	return c.suspended
}

func (c *wasapiContext) Err() error {
	return c.err.Load()
}

// isWASAPIDeviceTransientError reports whether err from (re)starting the client
// indicates a temporarily unusable device rather than a permanent failure.
func isWASAPIDeviceTransientError(err error) bool {
	return errors.Is(err, errDeviceNotFound) ||
		errors.Is(err, _AUDCLNT_E_DEVICE_INVALIDATED) ||
		errors.Is(err, _AUDCLNT_E_RESOURCES_INVALIDATED) ||
		errors.Is(err, _RPC_E_DISCONNECTED)
}

func (c *wasapiContext) restart() error {
	// The device is temporarily unusable, e.g. a headset was unplugged or the
	// machine is resuming from sleep. Recreate it, retrying transient failures a
	// bounded number of times.
	//
	// The mux is drained at ~real time so recovery resumes near the current
	// position instead of replaying a backlog, while the re-acquisition itself
	// backs off so a longer outage does not hammer the audio service.
	var buf [4096]float32
	framesPerRead := len(buf) / c.channelCount
	perRead := time.Duration(float64(time.Second) * float64(framesPerRead) / float64(c.sampleRate))

	reacquireInterval := wasapiReacquireMinInterval
	var nextReacquire time.Time // zero value: attempt immediately
	var retryCount int

	for {
		// Pause retrying entirely while suspended, and reset the retry count on
		// resume: a long suspend must not make the next resume give up
		// immediately.
		c.suspendedCond.L.Lock()
		var wasSuspended bool
		for c.suspended {
			wasSuspended = true
			c.suspendedCond.Wait()
		}
		c.suspendedCond.L.Unlock()
		if wasSuspended {
			reacquireInterval = wasapiReacquireMinInterval
			nextReacquire = time.Time{}
			retryCount = 0
		}

		if now := time.Now(); !now.Before(nextReacquire) {
			err := c.initialize()
			if err == nil {
				return nil
			}
			if !isWASAPIDeviceTransientError(err) {
				return err
			}
			retryCount++
			if retryCount >= wasapiRestartRetryLimit {
				return err
			}
			nextReacquire = time.Now().Add(reacquireInterval)
			reacquireInterval = min(reacquireInterval*2, wasapiReacquireMaxInterval)
		}

		c.mux.ReadFloat32s(buf[:framesPerRead*c.channelCount])
		c.mixed += int64(framesPerRead)
		time.Sleep(perRead)
	}
}
