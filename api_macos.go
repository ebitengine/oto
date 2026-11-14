// Copyright 2026 The Oto Authors
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

//go:build darwin && !ios

package oto

import (
	"structs"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	kAudioObjectSystemObject = 1

	kAudioObjectPropertyScopeGlobal = 0x676c6f62 // 'glob'
	kAudioObjectPropertyScopeOutput = 0x6f757470 // 'outp'
	kAudioObjectPropertyElementMain = 0

	kAudioHardwarePropertyDefaultOutputDevice = 0x644f7574 // 'dOut'
	kAudioDevicePropertyLatency               = 0x6c746e63 // 'ltnc'
	kAudioDevicePropertyStreams               = 0x73746d23 // 'stm#'
	kAudioDevicePropertyNominalSampleRate     = 0x6e737274 // 'nsrt'
	kAudioStreamPropertyLatency               = 0x6c746e63 // 'ltnc'
)

type _AudioObjectPropertyAddress struct {
	_         structs.HostLayout
	mSelector uint32
	mScope    uint32
	mElement  uint32
}

// initializeSessionAPI is a no-op on macOS: AVAudioSession does not exist there.
func initializeSessionAPI() {}

func initializeOutputLatencyAPI() error {
	coreaudio, err := purego.Dlopen("/System/Library/Frameworks/CoreAudio.framework/CoreAudio", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&_AudioObjectGetPropertyData, coreaudio, "AudioObjectGetPropertyData")
	return nil
}

var _AudioObjectGetPropertyData func(inObjectID uint32, inAddress *_AudioObjectPropertyAddress, inQualifierDataSize uint32, inQualifierData unsafe.Pointer, ioDataSize *uint32, outData unsafe.Pointer) uintptr

// audioObjectPropertyData reads a property of an audio object into data, and reports whether it succeeded.
// For a property holding an array, data receives its first element.
func audioObjectPropertyData[T any](object uint32, selector uint32, scope uint32, data *T) bool {
	address := _AudioObjectPropertyAddress{
		mSelector: selector,
		mScope:    scope,
		mElement:  kAudioObjectPropertyElementMain,
	}
	size := uint32(unsafe.Sizeof(*data))
	return _AudioObjectGetPropertyData(object, &address, 0, nil, &size, unsafe.Pointer(data)) == noErr
}

// outputLatency returns how long the default output device takes to play a frame after the frame's output time,
// or 0 if it is unknown.
func outputLatency() time.Duration {
	var device uint32
	if !audioObjectPropertyData(kAudioObjectSystemObject, kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, &device) {
		return 0
	}
	var sampleRate float64
	if !audioObjectPropertyData(device, kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, &sampleRate) || !(sampleRate > 0) {
		return 0
	}
	var deviceLatency uint32
	if !audioObjectPropertyData(device, kAudioDevicePropertyLatency, kAudioObjectPropertyScopeOutput, &deviceLatency) {
		return 0
	}
	// The output stream adds its own latency to the device's.
	var stream uint32
	var streamLatency uint32
	if !audioObjectPropertyData(device, kAudioDevicePropertyStreams, kAudioObjectPropertyScopeOutput, &stream) ||
		!audioObjectPropertyData(stream, kAudioStreamPropertyLatency, kAudioObjectPropertyScopeGlobal, &streamLatency) {
		streamLatency = 0
	}
	return time.Duration(float64(deviceLatency+streamLatency) / sampleRate * float64(time.Second))
}
