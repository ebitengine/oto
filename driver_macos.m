// Copyright 2020 The Oto Authors
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

// +build darwin,!ios,!js

#import <AppKit/AppKit.h>

#include "_cgo_export.h"

@interface OtoInterruptObserver : NSObject {
}

@property(nonatomic) AudioQueueRef audioQueue;

@end

@implementation OtoInterruptObserver {
  AudioQueueRef _audioQueue;
}

- (void)receiveSleepNote:(NSNotification *)note {
  OSStatus status = AudioQueuePause([self audioQueue]);
  if (status != noErr) {
    oto_setErrorByNotification(status, "AudioQueuePause");
  }
}

- (void)receiveWakeNote:(NSNotification *)note {
  OSStatus status = AudioQueueStart([self audioQueue], nil);
  if (status != noErr) {
    oto_setErrorByNotification(status, "AudioQueueStart");
  }
}

@end

// oto_setNotificationHandler sets a handler for interruption events.
void oto_setNotificationHandler(AudioQueueRef audioQueue) {
  OtoInterruptObserver *observer = [[OtoInterruptObserver alloc] init];
  observer.audioQueue = audioQueue;

  [[[NSWorkspace sharedWorkspace] notificationCenter]
      addObserver:observer
         selector:@selector(receiveSleepNote:)
             name:NSWorkspaceWillSleepNotification
           object:NULL];
  [[[NSWorkspace sharedWorkspace] notificationCenter]
      addObserver:observer
         selector:@selector(receiveWakeNote:)
             name:NSWorkspaceDidWakeNotification
           object:NULL];
}
