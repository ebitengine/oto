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
			return 0, nil, fmt.Errorf("oto: AudioQueueAllocateBuffer failed: %d", osstatus)
		}
		buf.mAudioDataByteSize = uint32(oneBufferSizeInBytes)
		bufs = append(bufs, buf)
	}

	return audioQueue, bufs, nil
}

type context struct {
	audioQueue      _AudioQueueRef
	unqueuedBuffers []_AudioQueueBufferRef

	oneBufferSizeInBytes int

	cond *sync.Cond

	toPause  bool
	toResume bool

	mux *mux.Mux
	err atomicError
}

// TODO: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

var theContext *context

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int) (*context, chan struct{}, error) {
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
		oneBufferSizeInBytes: oneBufferSizeInBytes,
	}
	theContext = c

	if err := initializeAPI(); err != nil {
		return nil, nil, err
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		var readyClosed bool
		defer func() {
			if !readyClosed {
				close(ready)
			}
		}()

		q, bs, err := newAudioQueue(sampleRate, channelCount, oneBufferSizeInBytes)
		if err != nil {
			c.err.TryStore(err)
			return
		}
		c.audioQueue = q
		c.unqueuedBuffers = bs

		var retryCount int
	try:
		if osstatus := _AudioQueueStart(c.audioQueue, nil); osstatus != noErr {
			if osstatus == avAudioSessionErrorCodeCannotStartPlaying && retryCount < 100 {
				// TODO: use sleepTime() after investigating when this error happens.
				time.Sleep(10 * time.Millisecond)
				retryCount++
				goto try
			}
			c.err.TryStore(fmt.Errorf("oto: AudioQueueStart failed at newContext: %d", osstatus))
			return
		}

		close(ready)
		readyClosed = true

		c.loop()
	}()

	return c, ready, nil
}

func (c *context) wait() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for len(c.unqueuedBuffers) == 0 && c.err.Load() == nil && !c.toPause && !c.toResume {
		c.cond.Wait()
	}
	return c.err.Load() == nil
}

func (c *context) loop() {
	buf32 := make([]float32, c.oneBufferSizeInBytes/4)
	for {
		if !c.wait() {
			return
		}
		c.appendBuffer(buf32)
	}
}

func (c *context) appendBuffer(buf32 []float32) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if c.err.Load() != nil {
		return
	}

	if c.toPause {
		if err := c.pause(); err != nil {
			c.err.TryStore(err)
		}
		c.toPause = false
		return
	}

	if c.toResume {
		if err := c.resume(); err != nil {
			c.err.TryStore(err)
		}
		c.toResume = false
		return
	}

	buf := c.unqueuedBuffers[0]
	copy(c.unqueuedBuffers, c.unqueuedBuffers[1:])
	c.unqueuedBuffers = c.unqueuedBuffers[:len(c.unqueuedBuffers)-1]

	c.mux.ReadFloat32s(buf32)
	copy(unsafe.Slice((*float32)(unsafe.Pointer(buf.mAudioData)), buf.mAudioDataByteSize/float32SizeInBytes), buf32)

	if osstatus := _AudioQueueEnqueueBuffer(c.audioQueue, buf, 0, nil); osstatus != noErr {
		c.err.TryStore(fmt.Errorf("oto: AudioQueueEnqueueBuffer failed: %d", osstatus))
	}
}

func (c *context) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	c.toPause = true
	c.toResume = false
	c.cond.Signal()
	return nil
}

func (c *context) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	c.toPause = false
	c.toResume = true
	c.cond.Signal()
	return nil
}

func (c *context) pause() error {
	if osstatus := _AudioQueuePause(c.audioQueue); osstatus != noErr {
		return fmt.Errorf("oto: AudioQueuePause failed: %d", osstatus)
	}
	return nil
}

func (c *context) resume() error {
	var retryCount int
try:
	if osstatus := _AudioQueueStart(c.audioQueue, nil); osstatus != noErr {
		if (osstatus == avAudioSessionErrorCodeCannotStartPlaying ||
			osstatus == avAudioSessionErrorCodeCannotInterruptOthers) &&
			retryCount < 30 {
			// It is uncertain that this error is temporary or not. Then let's use exponential-time sleeping.
			time.Sleep(sleepTime(retryCount))
			retryCount++
			goto try
		}
		if osstatus == avAudioSessionErrorCodeSiriIsRecording {
			// As this error should be temporary, it should be OK to use a short time for sleep anytime.
			time.Sleep(10 * time.Millisecond)
			goto try
		}
		return fmt.Errorf("oto: AudioQueueStart failed at Resume: %d", osstatus)
	}
	return nil
}

func (c *context) Err() error {
	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	return nil
}

func render(inUserData unsafe.Pointer, inAQ _AudioQueueRef, inBuffer _AudioQueueBufferRef) {
	theContext.cond.L.Lock()
	defer theContext.cond.L.Unlock()
	theContext.unqueuedBuffers = append(theContext.unqueuedBuffers, inBuffer)
	theContext.cond.Signal()
}

func sleepTime(count int) time.Duration {
	switch count {
	case 0:
		return 10 * time.Millisecond
	case 1:
		return 20 * time.Millisecond
	case 2:
		return 50 * time.Millisecond
	default:
		return 100 * time.Millisecond
	}
}
