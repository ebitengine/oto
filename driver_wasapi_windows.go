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
	players *players

	comThread *comThread
	err       atomicError

	sampleReadyEvent windows.Handle
	client           *_IAudioClient2
	mixFormat        *_WAVEFORMATEX
	bufferFrames     uint32
	renderClient     *_IAudioRenderClient
}

func newWASAPIContext(sampleRate, channelCount int, players *players) (*wasapiContext, chan struct{}, error) {
	t, err := newCOMThread()
	if err != nil {
		return nil, nil, err
	}

	c := &wasapiContext{
		players:   players,
		comThread: t,
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
	println(c.client)

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

	// Note that the caller is responsible to free f.
	// As f lives until the process dies, f's lifetime doesn't have to be cared.
	f, err := c.client.GetMixFormat()
	if err != nil {
		return err
	}
	c.mixFormat = f

	if err := c.client.Initialize(_AUDCLNT_SHAREMODE_SHARED,
		_AUDCLNT_STREAMFLAGS_EVENTCALLBACK|_AUDCLNT_STREAMFLAGS_NOPERSIST,
		0, 0, f, nil); err != nil {
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

	// TODO: Some errors should be allowed? See WASAPIManager.cpp.

	/*defaultDevicePeriod, _, err := c.client.GetDevicePeriod()
	if err != nil {
		return err
	}

	devicePeriodInSeconds := float64(defaultDevicePeriod) / _REFTIMES_PER_SEC
	framesPerPeriod := uint32(float64(c.mixFormat.nSamplesPerSec)*devicePeriodInSeconds + 0.5)*/

	if err := c.client.Start(); err != nil {
		return err
	}

	// ...
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := _CoInitializeEx(nil, _COINIT_MULTITHREADED); err != nil {
			c.client.Stop()
			c.err.TryStore(err)
			return
		}
		defer _CoUninitialize()

		c.loopOnRenderThread()
	}()

	return nil
}

func (c *wasapiContext) loopOnRenderThread() {
	for {
		evt, err := windows.WaitForSingleObject(c.sampleReadyEvent, 2000)
		if err != nil {
			c.client.Stop()
			c.err.TryStore(fmt.Errorf("oto: WaitForSingleObject failed: %w", err))
			return
		}
		if evt != windows.WAIT_OBJECT_0 {
			c.client.Stop()
			c.err.TryStore(fmt.Errorf("oto: WaitForSingleObject failed: returned value: %d", evt))
			return
		}

		// See OnAudioSampleRequested
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
