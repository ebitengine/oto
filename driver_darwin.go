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

// #cgo LDFLAGS: -framework AudioToolbox
//
// #import <AudioToolbox/AudioToolbox.h>
//
// void oto_render(void* inUserData, AudioQueueRef inAQ, AudioQueueBufferRef inBuffer);
//
// void oto_setNotificationHandler();
import "C"

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

const (
	float32SizeInBytes = 4
)

const (
	avAudioSessionErrorCodeCannotStartPlaying = 0x21706c61 // '!pla'
	avAudioSessionErrorCodeSiriIsRecording    = 0x73697269 // 'siri'
)

func newAudioQueue(sampleRate, channelCount, bitDepthInBytes int) (C.AudioQueueRef, []C.AudioQueueBufferRef, error) {
	desc := C.AudioStreamBasicDescription{
		mSampleRate:       C.double(sampleRate),
		mFormatID:         C.kAudioFormatLinearPCM,
		mFormatFlags:      C.kAudioFormatFlagIsFloat,
		mBytesPerPacket:   C.UInt32(channelCount * float32SizeInBytes),
		mFramesPerPacket:  1,
		mBytesPerFrame:    C.UInt32(channelCount * float32SizeInBytes),
		mChannelsPerFrame: C.UInt32(channelCount),
		mBitsPerChannel:   C.UInt32(8 * float32SizeInBytes),
	}

	var audioQueue C.AudioQueueRef
	if osstatus := C.AudioQueueNewOutput(
		&desc,
		(C.AudioQueueOutputCallback)(C.oto_render),
		nil,
		(C.CFRunLoopRef)(0),
		(C.CFStringRef)(0),
		0,
		&audioQueue); osstatus != C.noErr {
		return nil, nil, fmt.Errorf("oto: AudioQueueNewFormat with StreamFormat failed: %d", osstatus)
	}

	bufs := make([]C.AudioQueueBufferRef, 0, 4)
	for len(bufs) < cap(bufs) {
		var buf C.AudioQueueBufferRef
		if osstatus := C.AudioQueueAllocateBuffer(audioQueue, bufferSizeInBytes, &buf); osstatus != C.noErr {
			return nil, nil, fmt.Errorf("oto: AudioQueueAllocateBuffer failed: %d", osstatus)
		}
		buf.mAudioDataByteSize = bufferSizeInBytes
		bufs = append(bufs, buf)
	}

	return audioQueue, bufs, nil
}

type context struct {
	sampleRate      int
	channelCount    int
	bitDepthInBytes int

	audioQueue      C.AudioQueueRef
	unqueuedBuffers []C.AudioQueueBufferRef

	cond *sync.Cond

	players *players
	err     atomicError
}

// TOOD: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

var theContext *context

func newContext(sampleRate, channelCount, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		sampleRate:      sampleRate,
		channelCount:    channelCount,
		bitDepthInBytes: bitDepthInBytes,
		cond:            sync.NewCond(&sync.Mutex{}),
		players:         newPlayers(),
	}
	theContext = c

	q, bs, err := newAudioQueue(sampleRate, channelCount, bitDepthInBytes)
	if err != nil {
		return nil, nil, err
	}
	c.audioQueue = q
	c.unqueuedBuffers = bs

	C.oto_setNotificationHandler()

	var retryCount int
try:
	if osstatus := C.AudioQueueStart(c.audioQueue, nil); osstatus != C.noErr {
		if osstatus == avAudioSessionErrorCodeCannotStartPlaying && retryCount < 100 {
			time.Sleep(10 * time.Millisecond)
			retryCount++
			goto try
		}
		return nil, nil, fmt.Errorf("oto: AudioQueueStart failed at newContext: %d", osstatus)
	}

	go c.loop()

	return c, ready, nil
}

func (c *context) wait() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for len(c.unqueuedBuffers) == 0 && c.err.Load() == nil {
		c.cond.Wait()
	}
	return c.err.Load() == nil
}

func (c *context) loop() {
	buf32 := make([]float32, bufferSizeInBytes/4)
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

	buf := c.unqueuedBuffers[0]
	copy(c.unqueuedBuffers, c.unqueuedBuffers[1:])
	c.unqueuedBuffers = c.unqueuedBuffers[:len(c.unqueuedBuffers)-1]

	c.players.read(buf32)
	for i, f := range buf32 {
		*(*float32)(unsafe.Pointer(uintptr(buf.mAudioData) + uintptr(i)*float32SizeInBytes)) = f
	}

	if osstatus := C.AudioQueueEnqueueBuffer(c.audioQueue, buf, 0, nil); osstatus != C.noErr {
		c.err.TryStore(fmt.Errorf("oto: AudioQueueEnqueueBuffer failed: %d", osstatus))
	}
}

func (c *context) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	if osstatus := C.AudioQueuePause(c.audioQueue); osstatus != C.noErr {
		return fmt.Errorf("oto: AudioQueuePause failed: %d", osstatus)
	}
	return nil
}

func (c *context) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	var retryCount int
try:
	if osstatus := C.AudioQueueStart(c.audioQueue, nil); osstatus != C.noErr {
		if osstatus == avAudioSessionErrorCodeCannotStartPlaying && retryCount < 100 {
			time.Sleep(10 * time.Millisecond)
			retryCount++
			goto try
		}
		if osstatus == avAudioSessionErrorCodeSiriIsRecording {
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

//export oto_render
func oto_render(inUserData unsafe.Pointer, inAQ C.AudioQueueRef, inBuffer C.AudioQueueBufferRef) {
	theContext.cond.L.Lock()
	defer theContext.cond.L.Unlock()
	theContext.unqueuedBuffers = append(theContext.unqueuedBuffers, inBuffer)
	theContext.cond.Signal()
}

//export oto_setGlobalPause
func oto_setGlobalPause() {
	theContext.Suspend()
}

//export oto_setGlobalResume
func oto_setGlobalResume() {
	theContext.Resume()
}
