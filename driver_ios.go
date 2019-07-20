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

// +build ios

package oto

// #cgo CFLAGS: -x objective-c
// #cgo LDFLAGS: -framework Foundation -framework OpenAL -framework AVFoundation
//
// #import <AVFoundation/AVFoundation.h>
// #include <OpenAL/al.h>
// #include <OpenAL/alc.h>
//
// @interface OtoInterruptObserver : NSObject {
// }
//
// - (void) onAudioSessionEvent: (NSNotification*)notification;
//
// @end
//
// @implementation OtoInterruptObserver
//
// ALCcontext* alcContext_ = NULL;
//
// - (void) onAudioSessionEvent: (NSNotification *) notification
// {
//   if (![notification.name isEqualToString:AVAudioSessionInterruptionNotification]) {
//     return;
//   }
//
//   NSObject* value = [notification.userInfo valueForKey:AVAudioSessionInterruptionTypeKey];
//   AVAudioSessionInterruptionType interruptionType = [value intValue];
//   switch (interruptionType) {
//   case AVAudioSessionInterruptionTypeBegan:
//     alcContext_ = alcGetCurrentContext();
//     alcMakeContextCurrent(NULL);
//     break;
//   case AVAudioSessionInterruptionTypeEnded:
//     alcMakeContextCurrent(alcContext_);
//     alcProcessContext(alcContext_);
//     break;
//   default:
//     NSAssert(NO, @"unexpected AVAudioSessionInterruptionType: %d", interruptionType);
//     break;
//   }
// }
//
// @end
//
// static void initialize() {
//   AVAudioSession* session = [AVAudioSession sharedInstance];
//   [[NSNotificationCenter defaultCenter] addObserver: [[OtoInterruptObserver alloc] init]
//                                            selector: @selector(onAudioSessionEvent:)
//                                                name: AVAudioSessionInterruptionNotification
//                                              object: session];
// }
import "C"

func init() {
	C.initialize()
}
