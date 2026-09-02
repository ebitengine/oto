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

package oto_test

import (
	"testing"
)

// Suspend and Resume return before the audio device is actually paused or resumed,
// but the requested state must be recorded in the caller's program order.
func TestSuspendResumeOrder(t *testing.T) {
	t.Cleanup(func() {
		if err := theContext.Resume(); err != nil {
			t.Error(err)
		}
	})

	if err := theContext.Suspend(); err != nil {
		t.Fatal(err)
	}
	if got, want := theContext.SuspendRequestedForTesting(), true; got != want {
		t.Errorf("suspend requested after Suspend: got %t, want %t", got, want)
	}

	if err := theContext.Resume(); err != nil {
		t.Fatal(err)
	}
	if got, want := theContext.SuspendRequestedForTesting(), false; got != want {
		t.Errorf("suspend requested after Suspend and Resume: got %t, want %t", got, want)
	}
}
