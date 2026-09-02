// Copyright 2021 The Oto Authors
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
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/oto/v3/internal/mux"
)

const (
	float32SizeInBytes = 4

	bufferCount = 4

	noErr = 0
)

func newAudioQueue(sampleRate, channelCount int, oneBufferSizeInBytes int) (_AudioQueueRef, []_AudioQueueBufferRef, error) {
	desc := _AudioStreamBasicDescription{
		mSampleRate:       float64(sampleRate),
		mFormatID:         uint32(kAudioFormatLinearPCM),
		mFormatFlags:      uint32(kAudioFormatFlagIsFloat),
		mBytesPerPacket:   uint32(channelCount * float32SizeInBytes),
		mFramesPerPacket:  1,
		mBytesPerFrame:    uint32(channelCount * float32SizeInBytes),
		mChannelsPerFrame: uint32(channelCount),
		mBitsPerChannel:   uint32(8 * float32SizeInBytes),
	}

	var audioQueue _AudioQueueRef
	if osstatus := _AudioQueueNewOutput(
		&desc,
		render,
		nil,
		0, //CFRunLoopRef
		0, //CFStringRef
		0,
		&audioQueue); osstatus != noErr {
		return 0, nil, fmt.Errorf("oto: AudioQueueNewFormat with StreamFormat failed: %d", osstatus)
	}

	bufs := make([]_AudioQueueBufferRef, 0, bufferCount)
	for len(bufs) < cap(bufs) {
		var buf _AudioQueueBufferRef
		if osstatus := _AudioQueueAllocateBuffer(audioQueue, uint32(oneBufferSizeInBytes), &buf); osstatus != noErr {
			// Disposing the queue also frees the buffers allocated so far.
			_ = _AudioQueueDispose(audioQueue, true)
			return 0, nil, fmt.Errorf("oto: AudioQueueAllocateBuffer failed: %d", osstatus)
		}
		buf.mAudioDataByteSize = uint32(oneBufferSizeInBytes)
		bufs = append(bufs, buf)
	}

	return audioQueue, bufs, nil
}

// queueState is the actual state of the AudioQueue.
type queueState int

const (
	// queueStateStopped indicates that the AudioQueue is not running and a start attempt
	// may be made at any time.
	queueStateStopped queueState = iota

	// queueStateStartDeferred indicates that a start attempt failed with a temporary
	// error and the next attempt waits for a timer (deferStart) or an audio session
	// notification.
	queueStateStartDeferred

	// queueStateRunning indicates that the AudioQueue was started and has not been
	// paused or invalidated since.
	queueStateRunning
)

type context struct {
	audioQueue      _AudioQueueRef
	unqueuedBuffers []_AudioQueueBufferRef

	sampleRate           int
	channelCount         int
	oneBufferSizeInBytes int

	cond *sync.Cond

	// toSuspend indicates that Suspend was requested and the AudioQueue must not
	// run until Resume is requested. This is the desired state requested by the user,
	// and is independent of state, the actual state of the queue.
	toSuspend bool

	// state is the actual state of the AudioQueue.
	state queueState

	// toRebuildQueue indicates that the AudioQueue was invalidated and must be
	// recreated before the next start. This concerns the validity of the queue object
	// and is independent of state, which concerns the start/stop lifecycle.
	toRebuildQueue bool

	// startRetries is the number of consecutive start attempts that failed with a
	// temporary error.
	startRetries int

	// startRetryTimer is the pending timer scheduled by deferStart, or nil. It is
	// stopped and cleared when the deferral is ended by another path (endDeferStart).
	startRetryTimer *time.Timer

	mux *mux.Mux
	err atomicError
}

// TODO: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

var theContext *context

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, _ string) (*context, chan struct{}, error) {
	// defaultOneBufferSizeInBytes is the default buffer size in bytes.
	//
	// 12288 seems necessary at least on iPod touch (7th) and MacBook Pro 2020.
	// With 48000[Hz] stereo, the maximum delay is (12288*4[buffers] / 4 / 2)[samples] / 48000 [Hz] = 100[ms].
	// '4' is float32 size in bytes. '2' is a number of channels for stereo.
	const defaultOneBufferSizeInBytes = 12288

	var oneBufferSizeInBytes int
	if bufferSizeInBytes != 0 {
		oneBufferSizeInBytes = bufferSizeInBytes / bufferCount
	} else {
		oneBufferSizeInBytes = defaultOneBufferSizeInBytes
	}
	bytesPerSample := channelCount * 4
	oneBufferSizeInBytes = oneBufferSizeInBytes / bytesPerSample * bytesPerSample

	ready := make(chan struct{})

	c := &context{
		cond:                 sync.NewCond(&sync.Mutex{}),
		mux:                  mux.New(sampleRate, channelCount, format),
		sampleRate:           sampleRate,
		channelCount:         channelCount,
		oneBufferSizeInBytes: oneBufferSizeInBytes,
	}
	theContext = c

	if err := initializeAPI(); err != nil {
		return nil, nil, err
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		q, bs, err := newAudioQueue(c.sampleRate, c.channelCount, c.oneBufferSizeInBytes)
		if err != nil {
			c.err.Join(err)
			close(ready)
			return
		}
		c.initialize(q, bs)

		setupSessionNotifications()

		close(ready)

		c.loop()
	}()

	return c, ready, nil
}

// initialize sets the freshly created AudioQueue and attempts the first start.
// A temporary start failure is not an error: the start is deferred and retried by loop.
func (c *context) initialize(q _AudioQueueRef, bs []_AudioQueueBufferRef) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	c.audioQueue = q
	c.unqueuedBuffers = bs
	c.start()
}

func (c *context) wait() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.idle() && c.err.Load() == nil {
		c.cond.Wait()
	}
	return c.err.Load() == nil
}

// idle reports whether step has nothing to do for now: the actual queue state matches
// the requested state and no buffer is waiting to be filled. It must be kept consistent
// with step: whenever idle returns false, step must change some state.
// The caller must hold c.cond.L.
func (c *context) idle() bool {
	if c.toRebuildQueue {
		return false
	}
	if c.toSuspend {
		return c.state != queueStateRunning
	}
	switch c.state {
	case queueStateStopped:
		return false
	case queueStateRunning:
		return len(c.unqueuedBuffers) == 0
	default:
		return true
	}
}

func (c *context) loop() {
	buf32 := make([]float32, c.oneBufferSizeInBytes/4)
	for {
		if !c.wait() {
			return
		}
		c.step(buf32)
	}
}

func (c *context) step(buf32 []float32) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if c.err.Load() != nil {
		return
	}

	if c.toRebuildQueue {
		if err := c.rebuildAudioQueue(); err != nil {
			c.err.Join(err)
			return
		}
		c.toRebuildQueue = false
		if c.state == queueStateRunning {
			c.state = queueStateStopped
		}
		return
	}

	if c.toSuspend {
		if c.state == queueStateRunning {
			if err := c.pause(); err != nil {
				c.err.Join(err)
			}
		}
		return
	}

	switch c.state {
	case queueStateStopped:
		c.start()
		return
	case queueStateStartDeferred:
		return
	}

	if len(c.unqueuedBuffers) == 0 {
		return
	}

	buf := c.unqueuedBuffers[0]
	copy(c.unqueuedBuffers, c.unqueuedBuffers[1:])
	c.unqueuedBuffers = c.unqueuedBuffers[:len(c.unqueuedBuffers)-1]

	c.mux.ReadFloat32s(buf32)
	copy(unsafe.Slice((*float32)(unsafe.Pointer(buf.mAudioData)), buf.mAudioDataByteSize/float32SizeInBytes), buf32)

	if osstatus := _AudioQueueEnqueueBuffer(c.audioQueue, buf, 0, nil); osstatus != noErr {
		if osstatus == kAudioQueueErr_QueueInvalidated {
			// The queue was invalidated (typically a mediaserverd reset).
			// The audio just rendered into `buf` is dropped: at most one buffer of glitch.
			c.state = queueStateStopped
			c.toRebuildQueue = true
			return
		}
		c.err.Join(fmt.Errorf("oto: AudioQueueEnqueueBuffer failed: %d", osstatus))
	}
}

// Suspend returns immediately. The actual AudioQueuePause runs on a
// background goroutine so the calling thread (typically the platform UI
// thread) never blocks on AudioToolbox calls. Errors from the asynchronous
// transition surface via Err.
func (c *context) Suspend() error {
	err := c.err.Load()
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		c.toSuspend = true
		c.cond.Signal()
	}()
	return err
}

// Resume returns immediately. See Suspend for the rationale; AudioQueueStart
// runs on a background goroutine.
func (c *context) Resume() error {
	err := c.err.Load()
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		c.toSuspend = false
		// Attempt the start right away even if a retry is pending with a backoff.
		c.endDeferStart()
		c.startRetries = 0
		c.cond.Signal()
	}()
	return err
}

// pause pauses the AudioQueue and updates c.state.
// The caller must hold c.cond.L.
func (c *context) pause() error {
	if osstatus := _AudioQueuePause(c.audioQueue); osstatus != noErr {
		if osstatus == kAudioQueueErr_QueueInvalidated {
			c.state = queueStateStopped
			c.toRebuildQueue = true
			return nil
		}
		return fmt.Errorf("oto: AudioQueuePause failed: %d", osstatus)
	}
	c.state = queueStateStopped
	return nil
}

// start attempts to start the AudioQueue once.
//
// On success, c.state becomes queueStateRunning. When the start fails with a temporary error, such
// as an audio session that cannot be activated because the application is in the
// background, another application owns the audio session, or media services are
// restarting, the start is deferred: playback stays silent and the attempt is repeated
// until it succeeds (#285). Any other failure is fatal and recorded in c.err.
//
// The caller must hold c.cond.L.
func (c *context) start() {
	osstatus := _AudioQueueStart(c.audioQueue, nil)
	if osstatus == noErr {
		c.state = queueStateRunning
		c.startRetries = 0
		return
	}

	switch osstatus {
	case kAudioQueueErr_QueueInvalidated:
		// The queue died (typically a mediaserverd reset). Recreate it before the next
		// attempt.
		c.toRebuildQueue = true
		c.deferStart()
	case avAudioSessionErrorCodeCannotStartPlaying,
		avAudioSessionErrorCodeCannotInterruptOthers,
		avAudioSessionErrorCodeSiriIsRecording,
		avAudioSessionErrorCodeUnspecified,
		kAudioHardwareIllegalOperationError:
		// The audio session cannot be activated now. This state can last arbitrarily
		// long (e.g. as long as the application stays in the background), so no retry
		// limit applies.
		c.deferStart()
	default:
		c.err.Join(fmt.Errorf("oto: AudioQueueStart failed: %d", osstatus))
	}
}

// deferStart schedules the next start attempt after a backoff delay. Ending the
// deferral earlier, e.g. on an audio session notification, is done via endDeferStart.
// The caller must hold c.cond.L.
func (c *context) deferStart() {
	c.state = queueStateStartDeferred
	d := startRetryDelay(c.startRetries)
	c.startRetries++
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		if c.startRetryTimer != t {
			// The deferral this timer belonged to was already ended by endDeferStart.
			return
		}
		c.startRetryTimer = nil
		if c.state == queueStateStartDeferred {
			c.state = queueStateStopped
		}
		c.cond.Signal()
	})
	c.startRetryTimer = t
}

// endDeferStart ends a pending deferral, if any: the timer scheduled by deferStart is
// stopped and the state goes back to queueStateStopped so that the next start attempt
// can happen immediately.
// The caller must hold c.cond.L, and must call c.cond.Signal after completing all of
// its state changes.
func (c *context) endDeferStart() {
	if c.startRetryTimer != nil {
		c.startRetryTimer.Stop()
		c.startRetryTimer = nil
	}
	if c.state == queueStateStartDeferred {
		c.state = queueStateStopped
	}
}

// restartFromNotification requests an immediate start attempt in response to an audio
// session notification: any backoff pending from deferStart is canceled and the retry
// counter is reset. rebuild indicates that the AudioQueue must be recreated first.
//
// It returns immediately: an observer runs on the thread that posts the notification,
// the main thread for UIApplicationDidBecomeActiveNotification, and loop holds the
// lock across AudioToolbox calls.
func (c *context) restartFromNotification(rebuild bool) {
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()

		if rebuild {
			c.toRebuildQueue = true
		}
		c.endDeferStart()
		if !c.toSuspend {
			// An interruption stops the queue without notifying its owner, and whether
			// it is still running cannot be queried, so let loop start it again. A
			// start on a running queue returns noErr.
			c.state = queueStateStopped
		}
		c.startRetries = 0
		c.cond.Signal()
	}()
}

// rebuildAudioQueue disposes the current AudioQueue (which may already be invalid)
// and creates a fresh queue with new buffers. The new queue is left in the stopped
// state.
//
// The caller must hold c.cond.L.
func (c *context) rebuildAudioQueue() error {
	if c.audioQueue != 0 {
		// kAudioQueueErr_QueueInvalidated is expected here: that is the very case
		// being recovered from. Anything else is unexpected and worth surfacing.
		osstatus := _AudioQueueDispose(c.audioQueue, true)
		c.audioQueue = 0
		if osstatus != noErr && osstatus != kAudioQueueErr_QueueInvalidated {
			c.unqueuedBuffers = nil
			return fmt.Errorf("oto: AudioQueueDispose failed during rebuild: %d", osstatus)
		}
	}
	c.unqueuedBuffers = nil

	q, bs, err := newAudioQueue(c.sampleRate, c.channelCount, c.oneBufferSizeInBytes)
	if err != nil {
		return fmt.Errorf("oto: rebuilding AudioQueue failed: %w", err)
	}
	c.audioQueue = q
	c.unqueuedBuffers = bs
	return nil
}

func (c *context) Err() error {
	return c.err.Load()
}

func render(inUserData unsafe.Pointer, inAQ _AudioQueueRef, inBuffer _AudioQueueBufferRef) {
	theContext.cond.L.Lock()
	defer theContext.cond.L.Unlock()
	// Drop callbacks from a previously-disposed queue: after rebuildAudioQueue,
	// late-delivered callbacks for the old queue would otherwise inject stale
	// buffer pointers into c.unqueuedBuffers.
	if inAQ != theContext.audioQueue {
		return
	}
	theContext.unqueuedBuffers = append(theContext.unqueuedBuffers, inBuffer)
	theContext.cond.Signal()
}

func startRetryDelay(count int) time.Duration {
	switch {
	case count == 0:
		return 10 * time.Millisecond
	case count == 1:
		return 20 * time.Millisecond
	case count == 2:
		return 50 * time.Millisecond
	case count < 10:
		return 100 * time.Millisecond
	default:
		return time.Second
	}
}
