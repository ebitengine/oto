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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Avoid goroutines on Windows (hajimehoshi/ebiten#1768).
// Apparently, switching contexts might take longer than other platforms.

const headerBufferSize = 4096

type header struct {
	waveOut uintptr
	buffer  []float32
	waveHdr *wavehdr
}

func newHeader(waveOut uintptr, bufferSizeInBytes int) (*header, error) {
	h := &header{
		waveOut: waveOut,
		buffer:  make([]float32, bufferSizeInBytes/4),
	}
	h.waveHdr = &wavehdr{
		lpData:         uintptr(unsafe.Pointer(&h.buffer[0])),
		dwBufferLength: uint32(bufferSizeInBytes),
	}
	if err := waveOutPrepareHeader(waveOut, h.waveHdr); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *header) Write(data []float32) error {
	copy(h.buffer, data)
	if err := waveOutWrite(h.waveOut, h.waveHdr); err != nil {
		return err
	}
	return nil
}

func (h *header) IsQueued() bool {
	return h.waveHdr.dwFlags&whdrInqueue != 0
}

func (h *header) Close() error {
	return waveOutUnprepareHeader(h.waveOut, h.waveHdr)
}

type context struct {
	sampleRate      int
	channelNum      int
	bitDepthInBytes int

	waveOut uintptr
	headers []*header

	buf32 []float32

	players *players
	err     atomic.Value

	cond *sync.Cond
}

var theContext *context

func newContext(sampleRate, channelNum, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		sampleRate:      sampleRate,
		channelNum:      channelNum,
		bitDepthInBytes: bitDepthInBytes,
		players:         newPlayers(),
		cond:            sync.NewCond(&sync.Mutex{}),
	}
	theContext = c

	const bitsPerSample = 32
	nBlockAlign := c.channelNum * bitsPerSample / 8
	f := &waveformatex{
		wFormatTag:      waveFormatIEEEFloat,
		nChannels:       uint16(c.channelNum),
		nSamplesPerSec:  uint32(c.sampleRate),
		nAvgBytesPerSec: uint32(c.sampleRate * nBlockAlign),
		wBitsPerSample:  bitsPerSample,
		nBlockAlign:     uint16(nBlockAlign),
	}

	// TOOD: What about using an event instead of a callback? PortAudio and other libraries do that.
	w, err := waveOutOpen(f, waveOutOpenCallback)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		// TODO: No device was found. Return the dummy device (#77).
		// TODO: Retry to open the device when possible.
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, err
	}

	c.waveOut = w
	c.headers = make([]*header, 0, 6)
	for len(c.headers) < cap(c.headers) {
		h, err := newHeader(c.waveOut, headerBufferSize)
		if err != nil {
			return nil, nil, err
		}
		c.headers = append(c.headers, h)
	}

	c.buf32 = make([]float32, headerBufferSize/4)
	go c.loop()

	return c, ready, nil
}

func (c *context) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	if err := waveOutPause(c.waveOut); err != nil {
		return err
	}
	return nil
}

func (c *context) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	// TODO: Ensure at least one header is queued?

	if err := waveOutRestart(c.waveOut); err != nil {
		return err
	}
	return nil
}

func (c *context) Err() error {
	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	return nil
}

func (c *context) isHeaderAvailable() bool {
	for _, h := range c.headers {
		if !h.IsQueued() {
			return true
		}
	}
	return false
}

var waveOutOpenCallback = windows.NewCallback(func(hwo, uMsg, dwInstance, dwParam1, dwParam2 uintptr) uintptr {
	// Queuing a header in this callback might not work especially when a headset is connected or disconnected.
	// Just signal the condition vairable and don't do other things.
	const womDone = 0x3bd
	if uMsg != womDone {
		return 0
	}
	theContext.cond.Signal()
	return 0
})

func (c *context) waitUntilHeaderAvailable() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for !c.isHeaderAvailable() && c.err.Load() == nil {
		c.cond.Wait()
	}
	return c.err.Load() == nil
}

func (c *context) loop() {
	for {
		if !c.waitUntilHeaderAvailable() {
			return
		}
		c.appendBuffers()
	}
}

func (c *context) appendBuffers() {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if c.err.Load() != nil {
		return
	}

	c.players.read(c.buf32)

	for _, h := range c.headers {
		if h.IsQueued() {
			continue
		}

		if err := h.Write(c.buf32); err != nil {
			switch {
			case errors.Is(err, mmsyserrNomem):
				continue
			case errors.Is(err, windows.ERROR_NOT_FOUND):
				// This error can happen when e.g. a new HDMI connection is detected (#51).
				// TODO: Retry later.
			}
			c.err.Store(fmt.Errorf("oto: Queueing the header failed: %v", err))
		}
		return
	}
}
