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

// +build darwin,ios
// +build !js

package oto

// #cgo LDFLAGS: -framework Foundation -framework AVFoundation
//
// #import <AudioToolbox/AudioToolbox.h>
//
// void oto_setNotificationHandler(AudioQueueRef audioQueue);
import "C"

import (
	"fmt"
)

func setNotificationHandler(driver *driver) {
	C.oto_setNotificationHandler(driver.audioQueue)
}

func componentSubType() C.OSType {
	return C.kAudioUnitSubType_RemoteIO
}

//export oto_setErrorByNotification
func oto_setErrorByNotification(s C.OSStatus, from *C.char) {
	if theDriver.err != nil {
		return
	}

	gofrom := C.GoString(from)
	theDriver.err = fmt.Errorf("oto: %s at notification failed: %d", gofrom, s)
}
