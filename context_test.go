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
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/ebitengine/oto/v3"
)

func TestDurationToBufferSize(t *testing.T) {
	const maxInt = int(^uint(0) >> 1)
	tests := []struct {
		name         string
		duration     time.Duration
		sampleRate   int
		channelCount int
		want         int64
		wantErr      bool
	}{
		{
			name:         "default",
			sampleRate:   48000,
			channelCount: 2,
		},
		{
			name:         "stereo",
			duration:     10 * time.Millisecond,
			sampleRate:   48000,
			channelCount: 2,
			want:         3840,
		},
		{
			name:         "mono",
			duration:     10 * time.Millisecond,
			sampleRate:   44100,
			channelCount: 1,
			want:         1764,
		},
		{
			name:         "partial frame",
			duration:     time.Millisecond,
			sampleRate:   44100,
			channelCount: 2,
			want:         352,
		},
		{
			name:         "less than one frame",
			duration:     time.Nanosecond,
			sampleRate:   48000,
			channelCount: 2,
		},
		{
			name:         "seven hours",
			duration:     7 * time.Hour,
			sampleRate:   48000,
			channelCount: 2,
			want:         9676800000,
			wantErr:      strconv.IntSize == 32,
		},
		{
			name:         "maximum duration",
			duration:     time.Duration(math.MaxInt64),
			sampleRate:   48000,
			channelCount: 2,
			want:         3541774862152232,
			wantErr:      strconv.IntSize == 32,
		},
		{
			name:         "largest aligned int",
			duration:     time.Duration(maxInt / 8),
			sampleRate:   int(time.Second),
			channelCount: 2,
			want:         int64(maxInt / 8 * 8),
		},
		{
			name:         "aligned int overflow",
			duration:     time.Duration(maxInt/8 + 1),
			sampleRate:   int(time.Second),
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:         "alignment before int conversion",
			duration:     time.Duration(maxInt),
			sampleRate:   250000000,
			channelCount: 1,
			want:         int64(maxInt / 4 * 4),
		},
		{
			name:         "large sample rate with short duration",
			duration:     time.Nanosecond,
			sampleRate:   maxInt,
			channelCount: 2,
			want:         int64(maxInt) / int64(time.Second) * 8,
		},
		{
			name:         "large sample rate",
			duration:     time.Second,
			sampleRate:   maxInt,
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:         "frame count overflow",
			duration:     time.Duration(math.MaxInt64),
			sampleRate:   maxInt,
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:         "frame size overflow",
			duration:     time.Second,
			sampleRate:   1,
			channelCount: maxInt/4 + 1,
			wantErr:      true,
		},
		{
			name:         "negative duration",
			duration:     -time.Nanosecond,
			sampleRate:   48000,
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:         "zero sample rate",
			duration:     time.Second,
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:         "negative sample rate",
			duration:     time.Second,
			sampleRate:   -1,
			channelCount: 2,
			wantErr:      true,
		},
		{
			name:       "zero channels",
			duration:   time.Second,
			sampleRate: 48000,
			wantErr:    true,
		},
		{
			name:         "negative channels",
			duration:     time.Second,
			sampleRate:   48000,
			channelCount: -1,
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := oto.DurationToBufferSize(tt.duration, tt.sampleRate, tt.channelCount)
			if tt.wantErr {
				if err == nil {
					t.Errorf("DurationToBufferSize returned %d without an error", got)
				}
				return
			}
			if err != nil {
				t.Errorf("DurationToBufferSize failed: %v", err)
				return
			}
			if int64(got) != tt.want {
				t.Errorf("DurationToBufferSize = %d, want %d", got, tt.want)
			}
		})
	}
}
