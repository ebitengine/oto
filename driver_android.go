// Copyright 2021 The Oto Authors
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
	"github.com/hajimehoshi/oto/v2/internal/oboe"
)

type context struct {
	sampleRate      int
	channelCount    int
	bitDepthInBytes int

	players *players
}

func newContext(sampleRate int, channelCount int, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		sampleRate:      sampleRate,
		channelCount:    channelCount,
		bitDepthInBytes: bitDepthInBytes,
		players:         newPlayers(),
	}
	if err := oboe.Play(sampleRate, channelCount, bitDepthInBytes, c.players.read); err != nil {
		return nil, nil, err
	}
	return c, ready, nil
}

func (c *context) Suspend() error {
	return oboe.Suspend()
}

func (c *context) Resume() error {
	return oboe.Resume()
}

func (c *context) Err() error {
	return nil
}
