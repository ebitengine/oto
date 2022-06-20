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
	"github.com/hajimehoshi/oto/v2/internal/objc"
)

const bufferSizeInBytes = 2048

var _ = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/Versions/Current/AppKit", purego.RTLD_GLOBAL)

func init() {
	// Create the Observer object
	class := objc.AllocateClassPair(objc.GetClass("NSObject\x00"), "OtoNotificationObserver\x00", 0)
	class.AddMethod(objc.RegisterName("receiveSleepNote:\x00"), objc.IMP(oto_setGlobalPause), "v@:@\x00")
	class.AddMethod(objc.RegisterName("receiveWakeNote:\x00"), objc.IMP(oto_setGlobalResume), "v@:@\x00")
	class.Register()
}

// oto_setNotificationHandler sets a handler for sleep/wake
// notifications.
func oto_setNotificationHandler() {
	//OtoNotificationObserver *observer = [[OtoNotificationObserver alloc] init];
	observer := objc.Send(objc.Class(objc.Send(objc.GetClass("OtoNotificationObserver\x00"), objc.RegisterName("alloc\x00"))), objc.RegisterName("init\x00"))
	//id notificationCenter = [[NSWorkspace sharedWorkspace] notificationCenter];
	notificationCenter := objc.Send(objc.Class(objc.Send(objc.GetClass("NSWorkspace\x00"), objc.RegisterName("sharedWorkspace\x00"))), objc.RegisterName("notificationCenter\x00"))
	//[notificationCenter
	//      addObserver:observer
	//         selector:@selector(receiveSleepNote:)
	//             name:NSWorkspaceWillSleepNotification
	//           object:NULL];
	objc.Send(objc.Class(notificationCenter), objc.RegisterName("addObserver:selector:name:object:\x00"),
		observer,
		objc.RegisterName("receiveSleepNote:\x00"),
		// Dlsym returns a pointer to the object so dereference it
		*(*uintptr)(unsafe.Pointer(purego.Dlsym(purego.RTLD_DEFAULT, "NSWorkspaceWillSleepNotification"))),
		0,
	)
	//  [notificationCenter
	//      addObserver:observer
	//         selector:@selector(receiveWakeNote:)
	//             name:NSWorkspaceDidWakeNotification
	//           object:NULL];
	objc.Send(objc.Class(notificationCenter), objc.RegisterName("addObserver:selector:name:object:\x00"),
		observer,
		objc.RegisterName("receiveWakeNote:\x00"),
		// Dlsym returns a pointer to the object so dereference it
		*(*uintptr)(unsafe.Pointer(purego.Dlsym(purego.RTLD_DEFAULT, "NSWorkspaceDidWakeNotification"))),
		0,
	)
}
