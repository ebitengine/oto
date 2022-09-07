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

//go:build darwin && !ios
// +build darwin,!ios

package oto

import (
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const bufferSizeInBytes = 2048

var appkit = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/Versions/Current/AppKit", purego.RTLD_GLOBAL)

// setNotificationHandler sets a handler for sleep/wake notifications.
func setNotificationHandler() {
	// Create the Observer object
	class := objc.AllocateClassPair(objc.GetClass("NSObject\x00"), "OtoNotificationObserver\x00", 0)
	class.AddMethod(objc.RegisterName("receiveSleepNote:\x00"), objc.NewIMP(setGlobalPause), "v@:@\x00")
	class.AddMethod(objc.RegisterName("receiveWakeNote:\x00"), objc.NewIMP(setGlobalResume), "v@:@\x00")
	class.Register()

	observer := objc.ID(class).Send(objc.RegisterName("new\x00"))

	notificationCenter := objc.ID(objc.GetClass("NSWorkspace\x00")).Send(objc.RegisterName("sharedWorkspace\x00")).Send(objc.RegisterName("notificationCenter\x00"))
	notificationCenter.Send(objc.RegisterName("addObserver:selector:name:object:\x00"),
		observer,
		objc.RegisterName("receiveSleepNote:\x00"),
		// Dlsym returns a pointer to the object so dereference it
		*(*uintptr)(unsafe.Pointer(purego.Dlsym(appkit, "NSWorkspaceWillSleepNotification"))),
		0,
	)
	notificationCenter.Send(objc.RegisterName("addObserver:selector:name:object:\x00"),
		observer,
		objc.RegisterName("receiveWakeNote:\x00"),
		// Dlsym returns a pointer to the object so dereference it
		*(*uintptr)(unsafe.Pointer(purego.Dlsym(appkit, "NSWorkspaceDidWakeNotification"))),
		0,
	)
}
