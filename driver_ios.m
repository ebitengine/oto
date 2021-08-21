// Copyright 2021 The Ebiten Authors
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

void oto_setNotificationHandler() {
  // AVAudioSessionInterruptionNotification is not reliable on iOS. Rely on
  // applicationWillResignActive and applicationDidBecomeActive instead. See
  // https://stackoverflow.com/questions/24404463/ios-siri-not-available-does-not-return-avaudiosessioninterruptionoptionshouldre
  return;
}
