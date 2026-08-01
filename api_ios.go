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

package oto

import (
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// avAudioSessionInterruptionTypeEnded is the AVAudioSessionInterruptionType value
// indicating that an audio session interruption has ended.
const avAudioSessionInterruptionTypeEnded = 0

// Values of exported NSString constants for audio session related notifications.
// A zero value indicates that the constant is unavailable.
var (
	_UIApplicationDidBecomeActiveNotification         objc.ID
	_AVAudioSessionInterruptionNotification           objc.ID
	_AVAudioSessionInterruptionTypeKey                objc.ID
	_AVAudioSessionMediaServicesWereResetNotification objc.ID
)

func initializeSessionAPI() {
	if uikit, err := purego.Dlopen("/System/Library/Frameworks/UIKit.framework/UIKit", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err == nil {
		_UIApplicationDidBecomeActiveNotification = nsStringConstant(uikit, "UIApplicationDidBecomeActiveNotification")
	}

	// The AVAudioSession symbols live in AVFAudio, which AVFoundation reexports.
	// AVFoundation is available on every supported iOS version.
	if avfoundation, err := purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err == nil {
		_AVAudioSessionInterruptionNotification = nsStringConstant(avfoundation, "AVAudioSessionInterruptionNotification")
		_AVAudioSessionInterruptionTypeKey = nsStringConstant(avfoundation, "AVAudioSessionInterruptionTypeKey")
		_AVAudioSessionMediaServicesWereResetNotification = nsStringConstant(avfoundation, "AVAudioSessionMediaServicesWereResetNotification")
	}
}

// nsStringConstant returns the value of an exported NSString constant, or 0 if it is
// unavailable.
func nsStringConstant(lib uintptr, name string) objc.ID {
	ptr, err := purego.Dlsym(lib, name)
	if err != nil || ptr == 0 {
		return 0
	}
	return *(*objc.ID)(unsafe.Pointer(ptr))
}
