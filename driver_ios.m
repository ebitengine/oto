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

#import <AVFoundation/AVFoundation.h>
#import <AudioToolbox/AudioToolbox.h>
#import <UIKit/UIKit.h>

#include "_cgo_export.h"

@interface OtoNotificationObserver : NSObject {
}

- (void)onAudioSessionInterruption:(NSNotification *)notification;
- (void)onApplicationDidEnterBackground:(NSNotification *)notification;
- (void)onApplicationWillEnterForeground:(NSNotification *)notification;

@end

@implementation OtoNotificationObserver {
  int backgroundCount_;
  int prevBackgroundCount_;
}

- (void)updateState {
  if (prevBackgroundCount_ == 0 && backgroundCount_ == 1) {
    oto_setGlobalPause();
  }
  if (prevBackgroundCount_ == 1 && backgroundCount_ == 0) {
    oto_setGlobalResume();
  }
  prevBackgroundCount_ = backgroundCount_;
}

- (void)onAudioSessionInterruption:(NSNotification *)notification {
  if (![notification.name isEqualToString:AVAudioSessionInterruptionNotification]) {
    return;
  }

  NSObject* value = [notification.userInfo valueForKey:AVAudioSessionInterruptionTypeKey];
  AVAudioSessionInterruptionType interruptionType = [(NSNumber*)value intValue];
  switch (interruptionType) {
  case AVAudioSessionInterruptionTypeBegan: {
    backgroundCount_++;
    [self updateState];
    break;
  }
  case AVAudioSessionInterruptionTypeEnded: {
    backgroundCount_--;
    [self updateState];
    break;
  }
  default:
    NSAssert(NO, @"unexpected AVAudioSessionInterruptionType: %lu",
             (unsigned long)(interruptionType));
    break;
  }
}

- (void)onApplicationDidEnterBackground:(NSNotification *)notification {
  backgroundCount_++;
  [self updateState];
}

- (void)onApplicationWillEnterForeground:(NSNotification *)notification {
  backgroundCount_--;
  [self updateState];
}

@end

// oto_setNotificationHandler sets a handler for interruption events.
// Without the handler, Siri would stop the audio (#80).
void oto_setNotificationHandler(AudioQueueRef audioQueue) {
  AVAudioSession* session = [AVAudioSession sharedInstance];
  OtoNotificationObserver *observer = [[OtoNotificationObserver alloc] init];
  [[NSNotificationCenter defaultCenter]
      addObserver:observer
         selector:@selector(onAudioSessionInterruption:)
             name:AVAudioSessionInterruptionNotification
           object:session];
  [[NSNotificationCenter defaultCenter]
      addObserver:observer
         selector:@selector(onApplicationDidEnterBackground:)
             name:UIApplicationDidEnterBackgroundNotification
           object:session];
  [[NSNotificationCenter defaultCenter]
      addObserver:observer
         selector:@selector(onApplicationWillEnterForeground:)
             name:UIApplicationWillEnterForegroundNotification
           object:session];
}
