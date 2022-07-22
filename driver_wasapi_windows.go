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
	"fmt"
	"reflect"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type comThread struct {
	funcCh chan func()
}

func newCOMThread() (*comThread, error) {
	funcCh := make(chan func())
	errCh := make(chan error)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := _CoInitializeEx(nil, _COINIT_MULTITHREADED); err != nil {
			errCh <- err
		}
		defer _CoUninitialize()

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
	}, nil
}

func (c *comThread) Run(f func()) {
	ch := make(chan struct{})
	c.funcCh <- func() {
		f()
		close(ch)
	}
	<-ch
}

type wasapiContext struct {
	sampleRate   int
	channelCount int
	players      *players

	comThread *comThread
	err       atomicError

	sampleReadyEvent windows.Handle
	client           *_IAudioClient2
	mixFormat        *_WAVEFORMATEXTENSIBLE
	bufferFrames     uint32
	renderClient     *_IAudioRenderClient
}

func newWASAPIContext(sampleRate, channelCount int, players *players) (*wasapiContext, chan struct{}, error) {
	t, err := newCOMThread()
	if err != nil {
		return nil, nil, err
	}

	c := &wasapiContext{
		sampleRate:   sampleRate,
		channelCount: channelCount,
		players:      players,
		comThread:    t,
	}

	var cerr error
	t.Run(func() {
		if err := c.initOnCOMThread(); err != nil {
			cerr = err
			return
		}
	})
	if cerr != nil {
		return nil, nil, cerr
	}

	ready := make(chan struct{})
	close(ready)

	return c, ready, nil
}

func (c *wasapiContext) initOnCOMThread() error {
	e, err := _CoCreateInstance(&uuidMMDeviceEnumerator, nil, uint32(_CLSCTX_ALL), &uuidIMMDeviceEnumerator)
	if err != nil {
		return err
	}
	enumerator := (*_IMMDeviceEnumerator)(e)
	defer enumerator.Release()

	device, err := enumerator.GetDefaultAudioEndPoint(eRender, eConsole)
	if err != nil {
		return err
	}
	defer device.Release()

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
	// Stereo with 48000 [Hz] is likely supported, but mono and/or other sample rates are unlikely supported.
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
	closest, err := c.client.IsFormatSupported(_AUDCLNT_SHAREMODE_SHARED, f)
	if err != nil {
		return err
	}
	if closest != nil {
		return fmt.Errorf("oto: the specified format is not supported (there is the closest format instead)")
	}
	c.mixFormat = f

	if err := c.client.Initialize(_AUDCLNT_SHAREMODE_SHARED,
		_AUDCLNT_STREAMFLAGS_EVENTCALLBACK|_AUDCLNT_STREAMFLAGS_NOPERSIST,
		0, 0, c.mixFormat, nil); err != nil {
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

	ev, err := windows.CreateEventEx(nil, nil, 0, windows.EVENT_ALL_ACCESS)
	if err != nil {
		return err
	}
	c.sampleReadyEvent = ev

	if err := c.client.SetEventHandle(c.sampleReadyEvent); err != nil {
		return err
	}

	// TODO: Should some errors be allowed? See WASAPIManager.cpp in the official example SimpleWASAPIPlaySound.

	if err := c.client.Start(); err != nil {
		return err
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := _CoInitializeEx(nil, _COINIT_MULTITHREADED); err != nil {
			c.client.Stop()
			c.err.TryStore(err)
			return
		}
		defer _CoUninitialize()

		if err := c.loopOnRenderThread(); err != nil {
			c.client.Stop()
			c.err.TryStore(err)
			return
		}
	}()

	return nil
}

func (c *wasapiContext) loopOnRenderThread() error {
	var buf []float32
	for {
		evt, err := windows.WaitForSingleObject(c.sampleReadyEvent, 2000)
		if err != nil {
			return err
		}
		if evt != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("oto: WaitForSingleObject failed: returned value: %d", evt)
		}

		paddingFrames, err := c.client.GetCurrentPadding()
		if err != nil {
			return err
		}

		frames := c.bufferFrames - paddingFrames
		if frames <= 0 {
			continue
		}

		// Get the destination buffer.
		dstBuf, err := c.renderClient.GetBuffer(frames)
		if err != nil {
			return err
		}

		// Calculate the buffer size.
		buflen := int(frames) * c.channelCount
		if cap(buf) < buflen {
			buf = make([]float32, buflen)
		} else {
			buf = buf[:buflen]
		}

		// Read the buffer from the players.
		c.players.read(buf)

		// Copy the read buf to the destination buffer.
		var dst []float32
		h := (*reflect.SliceHeader)(unsafe.Pointer(&dst))
		h.Data = uintptr(unsafe.Pointer(dstBuf))
		h.Len = buflen
		h.Cap = buflen
		copy(dst, buf)

		// Release the buffer.
		if err := c.renderClient.ReleaseBuffer(frames, 0); err != nil {
			return err
		}

		buf = buf[:0]
	}
}

func (c *wasapiContext) Suspend() error {
	return nil
}

func (c *wasapiContext) Resume() error {
	return nil
}

func (c *wasapiContext) Err() error {
	return c.err.Load()
}
