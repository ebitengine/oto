// Copyright 2019 The Oto Authors
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

// +build !js

package oto

// #cgo LDFLAGS: -framework AudioToolbox
//
// #import <AudioToolbox/AudioToolbox.h>
//
// OSStatus oto_render(void *inRefCon,
//     AudioUnitRenderActionFlags *ioActionFlags,
//     AudioTimeStamp *inTimeStamp,
//     UInt32 inBusNumber,
//     UInt32 inNumberFrames,
//     AudioBufferList *ioData);
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

type driver struct {
	audioUnit       C.AudioUnit
	buf             []byte
	bufSize         int
	channelNum      int
	bitDepthInBytes int
	m               sync.Mutex
}

var (
	theDriver *driver
	driverM   sync.Mutex
)

func setDriver(d *driver) {
	driverM.Lock()
	defer driverM.Unlock()

	if theDriver != nil && d != nil {
		panic("oto: at most one driver object can exist")
	}
	theDriver = d

	setNotificationHandler(d)
}

func getDriver() *driver {
	driverM.Lock()
	defer driverM.Unlock()

	return theDriver
}

// TOOD: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

func newDriver(sampleRate, channelNum, bitDepthInBytes, bufferSizeInBytes int) (tryWriteCloser, error) {
	cd := C.AudioComponentDescription{
		componentType:         C.kAudioUnitType_Output,
		componentSubType:      componentSubType(),
		componentManufacturer: C.kAudioUnitManufacturer_Apple,
	}
	comp := C.AudioComponentFindNext(nil, &cd)
	if comp == nil {
		return nil, fmt.Errorf("oto: AudioComponentFindNext must not return nil")
	}

	var audioUnit C.AudioUnit
	if osstatus := C.AudioComponentInstanceNew(comp, &audioUnit); osstatus != C.noErr {
		return nil, fmt.Errorf("oto: AudioComponentInstanceNew failed: %d", osstatus)
	}

	flags := C.kAudioFormatFlagIsPacked
	if bitDepthInBytes != 1 {
		flags |= C.kAudioFormatFlagIsSignedInteger
	}
	desc := C.AudioStreamBasicDescription{
		mSampleRate:       C.double(sampleRate),
		mFormatID:         C.kAudioFormatLinearPCM,
		mFormatFlags:      C.UInt32(flags),
		mBytesPerPacket:   C.UInt32(channelNum * bitDepthInBytes),
		mFramesPerPacket:  1,
		mBytesPerFrame:    C.UInt32(channelNum * bitDepthInBytes),
		mChannelsPerFrame: C.UInt32(channelNum),
		mBitsPerChannel:   C.UInt32(8 * bitDepthInBytes),
	}
	if osstatus := C.AudioUnitSetProperty(
		audioUnit,
		C.kAudioUnitProperty_StreamFormat,
		C.kAudioUnitScope_Input,
		0,
		unsafe.Pointer(&desc),
		C.UInt32(unsafe.Sizeof(desc))); osstatus != C.noErr {
		return nil, fmt.Errorf("oto: AudioUnitSetProperty with StreamFormat failed: %d", osstatus)
	}

	d := &driver{
		audioUnit:       audioUnit,
		channelNum:      channelNum,
		bitDepthInBytes: bitDepthInBytes,
		bufSize:         bufferSizeInBytes,
	}
	// Set the driver before setting the rendering callback.
	setDriver(d)

	input := C.AURenderCallbackStruct{
		inputProc: C.AURenderCallback(C.oto_render),
	}
	if osstatus := C.AudioUnitSetProperty(
		audioUnit,
		C.kAudioUnitProperty_SetRenderCallback,
		C.kAudioUnitScope_Global,
		0,
		unsafe.Pointer(&input),
		C.UInt32(unsafe.Sizeof(input))); osstatus != C.noErr {
		return nil, fmt.Errorf("oto: AudioUnitSetProperty with SetRenderCallback failed: %d", osstatus)
	}

	if osstatus := C.AudioUnitInitialize(audioUnit); osstatus != C.noErr {
		return nil, fmt.Errorf("oto: AudioUnitInitialize failed: %d", osstatus)
	}

	if osstatus := C.AudioOutputUnitStart(audioUnit); osstatus != C.noErr {
		return nil, fmt.Errorf("oto: AudioOutputUnitStart failed: %d", osstatus)
	}
	return d, nil
}

//export oto_render
func oto_render(inRefCon unsafe.Pointer,
	ioActionFlags *C.AudioUnitRenderActionFlags,
	inTimeStamp *C.AudioTimeStamp,
	inBusNumber C.UInt32,
	inNumberFrames C.UInt32,
	ioData *C.AudioBufferList) C.OSStatus {

	d := getDriver()
	d.m.Lock()
	defer d.m.Unlock()

	s := d.channelNum * d.bitDepthInBytes
	n := int(inNumberFrames) * s
	if n > len(d.buf) {
		n = len(d.buf) / s * s
	}

	for i := 0; i < n; i++ {
		*(*byte)(unsafe.Pointer(uintptr(ioData.mBuffers[0].mData) + uintptr(i))) = d.buf[i]
	}
	d.buf = d.buf[n:]
	ioData.mBuffers[0].mDataByteSize = C.UInt32(n)

	return C.noErr
}

func (d *driver) TryWrite(data []byte) (int, error) {
	d.m.Lock()
	defer d.m.Unlock()

	n := d.bufSize - len(d.buf)
	if n > len(data) {
		n = len(data)
	}
	d.buf = append(d.buf, data[:n]...)
	return n, nil
}

func (d *driver) Close() error {
	d.m.Lock()
	defer d.m.Unlock()

	if osstatus := C.AudioOutputUnitStop(d.audioUnit); osstatus != C.noErr {
		return fmt.Errorf("oto: AudioOutputUnitStop failed: %d", osstatus)
	}
	if osstatus := C.AudioUnitUninitialize(d.audioUnit); osstatus != C.noErr {
		return fmt.Errorf("oto: AudioUnitUninitialize failed: %d", osstatus)
	}
	if osstatus := C.AudioComponentInstanceDispose(d.audioUnit); osstatus != C.noErr {
		return fmt.Errorf("oto: AudioComponentInstanceDispose failed: %d", osstatus)
	}
	d.audioUnit = nil
	setDriver(nil)
	return nil
}
