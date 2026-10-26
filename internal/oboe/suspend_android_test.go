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

package oboe_test

import (
	"testing"

	"github.com/ebitengine/oto/v3/internal/oboe"
)

// Ebitengine calls Suspend on lifecycle events as soon as NewContext returns,
// which can be before the context's goroutine calls Play.
func TestSuspendBeforePlay(t *testing.T) {
	if err := oboe.Suspend(); err != nil {
		t.Errorf("Suspend before Play: %v", err)
	}
}
