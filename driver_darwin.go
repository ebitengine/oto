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
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/hajimehoshi/oto/v2/internal/objc"
)

const (
	float32SizeInBytes = 4

	noErr = 0
)

const (
	avAudioSessionErrorCodeCannotStartPlaying = 0x21706c61 // '!pla'
	avAudioSessionErrorCodeSiriIsRecording    = 0x73697269 // 'siri'
)

const (
	kAudioFormatLinearPCM = 0x6C70636D //'lpcm'
)

const (
	kAudioFormatFlagIsFloat = 1 << 0 // 0x1
)

type _AudioStreamBasicDescription struct {
	mSampleRate       float64
	mFormatID         uint32
	mFormatFlags      uint32
	mBytesPerPacket   uint32
	mFramesPerPacket  uint32
	mBytesPerFrame    uint32
	mChannelsPerFrame uint32
	mBitsPerChannel   uint32
	mReserved         uint32
}

type _AudioQueueRef uintptr

type _AudioQueueBufferRef *_AudioQueueBuffer

type _AudioQueueBuffer struct {
	mAudioDataBytesCapacity uint32
	mAudioData              uintptr // void*
	mAudioDataByteSize      uint32
	mUserData               uintptr // void*

	mPacketDescriptionCapacity uint32
	mPacketDescriptions        uintptr // *AudioStreamPacketDescription
	mPacketDescriptionCount    uint32
}

var (
	toolbox                    = purego.Dlopen("/System/Library/Frameworks/AudioToolbox.framework/Versions/Current/AudioToolbox", purego.RTLD_GLOBAL)
	gpAudioQueueNewOutput      = purego.Dlsym(toolbox, "AudioQueueNewOutput")
	gpAudioQueueStart          = purego.Dlsym(toolbox, "AudioQueueStart")
	gpAudioQueuePause          = purego.Dlsym(toolbox, "AudioQueuePause")
	gpAudioQueueAllocateBuffer = purego.Dlsym(toolbox, "AudioQueueAllocateBuffer")
	gpAudioQueueEnqueueBuffer  = purego.Dlsym(toolbox, "AudioQueueEnqueueBuffer")
)

func newAudioQueue(sampleRate, channelCount, bitDepthInBytes int) (_AudioQueueRef, []_AudioQueueBufferRef, error) {
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
	if osstatus, _, _ := purego.SyscallN(gpAudioQueueNewOutput,
		uintptr(unsafe.Pointer(&desc)),
		objc.NewCallback(oto_render),
		0,
		0, //CFRunLoopRef
		0, //CFStringRef
		0,
		uintptr(unsafe.Pointer(&audioQueue))); osstatus != noErr {
		return 0, nil, fmt.Errorf("oto: AudioQueueNewFormat with StreamFormat failed: %d", osstatus)
	}

	bufs := make([]_AudioQueueBufferRef, 0, 4)
	for len(bufs) < cap(bufs) {
		var buf _AudioQueueBufferRef
		if osstatus, _, _ := purego.SyscallN(gpAudioQueueAllocateBuffer, uintptr(audioQueue), uintptr(bufferSizeInBytes), uintptr(unsafe.Pointer(&buf))); osstatus != noErr {
			return 0, nil, fmt.Errorf("oto: AudioQueueAllocateBuffer failed: %d", osstatus)
		}
		buf.mAudioDataByteSize = bufferSizeInBytes
		bufs = append(bufs, buf)
	}

	return audioQueue, bufs, nil
}

type context struct {
	sampleRate      int
	channelCount      int
	bitDepthInBytes int

	audioQueue      _AudioQueueRef
	unqueuedBuffers []_AudioQueueBufferRef

	cond *sync.Cond

	players *players
	err     atomicError
}

// TODO: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

var theContext *context

func newContext(sampleRate, channelCount, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		sampleRate:      sampleRate,
		channelCount:      channelCount,
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

	oto_setNotificationHandler()

	var retryCount int
try:
	if osstatus, _, _ := purego.SyscallN(gpAudioQueueStart, uintptr(c.audioQueue), 0); osstatus != noErr {
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
		*(*float32)(unsafe.Pointer(buf.mAudioData + uintptr(i)*float32SizeInBytes)) = f
	}

	if osstatus, _, _ := purego.SyscallN(gpAudioQueueEnqueueBuffer, uintptr(c.audioQueue), uintptr(unsafe.Pointer(buf)), 0, 0); osstatus != noErr {
		c.err.TryStore(fmt.Errorf("oto: AudioQueueEnqueueBuffer failed: %d", osstatus))
	}
}

func (c *context) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()
	
	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	if osstatus, _, _ := purego.SyscallN(gpAudioQueuePause, uintptr(c.audioQueue)); osstatus != noErr {
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
	if osstatus, _, _ := purego.SyscallN(gpAudioQueueStart, uintptr(c.audioQueue), 0); osstatus != noErr {
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

func oto_render(inUserData, inAQ, inBuffer unsafe.Pointer) {
	theContext.cond.L.Lock()
	defer theContext.cond.L.Unlock()
	theContext.unqueuedBuffers = append(theContext.unqueuedBuffers, _AudioQueueBufferRef(inBuffer))
	theContext.cond.Signal()
}

func oto_setGlobalPause(self uintptr, _cmd objc.SEL, notification uintptr) {
	theContext.Suspend()
}

func oto_setGlobalResume(self uintptr, _cmd objc.SEL, notification uintptr) {
	theContext.Resume()
}
