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

//go:build linux && !android && cgo

package oto

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ebitengine/oto/v3/internal/mux"
)

func TestNewContextPrefersALSAOnLinuxWithCgo(t *testing.T) {
	originalALSA := newALSAContextFunc
	originalPulseAudio := newPulseAudioContextFunc
	t.Cleanup(func() {
		newALSAContextFunc = originalALSA
		newPulseAudioContextFunc = originalPulseAudio
	})

	var order []string
	newALSAContextFunc = func(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (*alsaContext, error) {
		order = append(order, "alsa")
		return nil, errors.New("alsa failed")
	}
	newPulseAudioContextFunc = func(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (*pulseAudioContext, error) {
		order = append(order, "pulse")
		return &pulseAudioContext{}, nil
	}

	_, ready, err := newContext(48000, 2, mux.FormatFloat32LE, 0)
	if err != nil {
		t.Fatalf("newContext failed: %v", err)
	}
	<-ready

	if got, want := order, []string{"alsa", "pulse"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backend selection order = %v, want %v", got, want)
	}
}
