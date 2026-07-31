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

package oboe

// Disable AAudio (hajimehoshi/ebiten#1634).
// AAudio doesn't care about plugging in/out of a headphone.
// See https://github.com/google/oboe/wiki/TechNote_Disconnect

// #cgo CXXFLAGS: -std=c++17 -DOBOE_ENABLE_AAUDIO=0
// #cgo LDFLAGS: -llog -lOpenSLES -static-libstdc++
//
// #include "binding_android.h"
import "C"

import (
	"fmt"
	"unsafe"
)

var (
	theReadFunc  func(buf []float32)
	theErrorFunc func(err error)
)

// Play starts playing. errorFunc is called when playing stops for a reason
// that cannot be recovered from, which can happen at any time after Play
// returns.
func Play(sampleRate int, channelCount int, readFunc func(buf []float32), errorFunc func(err error), bufferSizeInBytes int) error {
	// Play can invoke the callbacks. Set the callbacks before Play.
	theReadFunc = readFunc
	theErrorFunc = errorFunc
	if msg := C.oto_oboe_Play(C.int(sampleRate), C.int(channelCount), C.int(bufferSizeInBytes)); msg != nil {
		return fmt.Errorf("oboe: Play failed: %s", C.GoString(msg))
	}
	return nil
}

func Suspend() error {
	if msg := C.oto_oboe_Suspend(); msg != nil {
		return fmt.Errorf("oboe: Suspend failed: %s", C.GoString(msg))
	}
	return nil
}

func Resume() error {
	if msg := C.oto_oboe_Resume(); msg != nil {
		return fmt.Errorf("oboe: Resume failed: %s", C.GoString(msg))
	}
	return nil
}

//export oto_oboe_read
func oto_oboe_read(buf *C.float, len C.size_t) {
	theReadFunc(unsafe.Slice((*float32)(unsafe.Pointer(buf)), len))
}

//export oto_oboe_error
func oto_oboe_error(msg *C.char) {
	theErrorFunc(fmt.Errorf("oboe: %s", C.GoString(msg)))
}
