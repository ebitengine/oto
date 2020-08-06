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
// +build !noaudio

#import <AVFoundation/AVFoundation.h>
#import <AudioToolbox/AudioToolbox.h>

#include "_cgo_export.h"

@interface OtoInterruptObserver : NSObject {
}

- (void) onAudioSessionEvent: (NSNotification*)notification;

@end

@implementation OtoInterruptObserver {
}

- (void) onAudioSessionEvent: (NSNotification *)notification
{
  if (![notification.name isEqualToString:AVAudioSessionInterruptionNotification]) {
    return;
  }

  NSObject* value = [notification.userInfo valueForKey:AVAudioSessionInterruptionTypeKey];
  AVAudioSessionInterruptionType interruptionType = [(NSNumber*)value intValue];
  switch (interruptionType) {
  case AVAudioSessionInterruptionTypeBegan: {
    oto_setGlobalPause(YES);
    break;
  }
  case AVAudioSessionInterruptionTypeEnded: {
    oto_setGlobalPause(NO);
    break;
  }
  default:
    NSAssert(NO, @"unexpected AVAudioSessionInterruptionType: %lu",
             (unsigned long)(interruptionType));
    break;
  }
}

@end

// oto_setNotificationHandler sets a handler for interruption events.
// Without the handler, Siri would stop the audio (#80).
void oto_setNotificationHandler(AudioQueueRef audioQueue) {
  AVAudioSession* session = [AVAudioSession sharedInstance];
  OtoInterruptObserver* observer = [[OtoInterruptObserver alloc] init];
  [[NSNotificationCenter defaultCenter] addObserver: observer
                                           selector: @selector(onAudioSessionEvent:)
                                               name: AVAudioSessionInterruptionNotification
                                             object: session];
}
