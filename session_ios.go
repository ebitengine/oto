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
	"sync"

	"github.com/ebitengine/purego/objc"
)

var setupSessionNotificationsOnce sync.Once

// setupSessionNotifications registers observers for audio session related notifications
// so that a deferred AudioQueue start is retried as soon as playback might be possible
// again. Failures are ignored: without observers, the timer in deferStart still retries.
func setupSessionNotifications() {
	setupSessionNotificationsOnce.Do(registerSessionNotificationObservers)
}

func registerSessionNotificationObservers() {
	class := objc.GetClass("NSNotificationCenter")
	if class == 0 {
		return
	}
	center := objc.ID(class).Send(objc.RegisterName("defaultCenter"))
	if center == 0 {
		return
	}
	selAddObserver := objc.RegisterName("addObserverForName:object:queue:usingBlock:")

	// The application became active. If a start was deferred because the application was
	// in the background or inactive, this is the moment it can succeed (#285).
	if name := _UIApplicationDidBecomeActiveNotification; name != 0 {
		block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
			theContext.restartFromNotification(false)
		})
		// The notification center copies the block, so this reference is unneeded.
		defer block.Release()
		center.Send(selAddObserver, name, objc.ID(0), objc.ID(0), block)
	}

	// Media services were reset (a mediaserverd restart). Every AudioQueue is dead and
	// must be recreated.
	if name := _AVAudioSessionMediaServicesWereResetNotification; name != 0 {
		block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
			theContext.restartFromNotification(true)
		})
		// The notification center copies the block, so this reference is unneeded.
		defer block.Release()
		center.Send(selAddObserver, name, objc.ID(0), objc.ID(0), block)
	}

	// An interruption (a phone call, Siri, another application's audio session) ended.
	// The system stops the AudioQueue at the beginning of an interruption, so a fresh
	// AudioQueueStart is needed even if the queue was believed to be running.
	if name, typeKey := _AVAudioSessionInterruptionNotification, _AVAudioSessionInterruptionTypeKey; name != 0 && typeKey != 0 {
		selUserInfo := objc.RegisterName("userInfo")
		selObjectForKey := objc.RegisterName("objectForKey:")
		selUnsignedIntegerValue := objc.RegisterName("unsignedIntegerValue")
		block := objc.NewBlock(func(_ objc.Block, notification objc.ID) {
			userInfo := notification.Send(selUserInfo)
			if userInfo == 0 {
				return
			}
			typ := userInfo.Send(selObjectForKey, typeKey)
			if typ == 0 {
				return
			}
			if objc.Send[uint64](typ, selUnsignedIntegerValue) != avAudioSessionInterruptionTypeEnded {
				return
			}
			theContext.restartFromNotification(false)
		})
		// The notification center copies the block, so this reference is unneeded.
		defer block.Release()
		center.Send(selAddObserver, name, objc.ID(0), objc.ID(0), block)
	}
}
