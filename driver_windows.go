// Copyright 2022 The Oto Authors
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
	"fmt"
)

type context struct {
	sampleRate      int
	channelCount    int
	bitDepthInBytes int

	players *players

	wasapiContext *wasapiContext
	winmmContext  *winmmContext
}

func newContext(sampleRate, channelCount, bitDepthInBytes int) (*context, chan struct{}, error) {
	p := newPlayers()
	ctx := &context{
		sampleRate:      sampleRate,
		channelCount:    channelCount,
		bitDepthInBytes: bitDepthInBytes,
		players:         p,
	}

	xc, ready, err0 := newWASAPIContext(sampleRate, channelCount, p)
	if err0 == nil {
		ctx.wasapiContext = xc
		return ctx, ready, nil
	}
	wc, ready, err1 := newWinMMContext(sampleRate, channelCount, p)
	if err1 == nil {
		ctx.winmmContext = wc
		return ctx, ready, nil
	}
	return nil, nil, fmt.Errorf("oto: initialization failed: WASAPI: %v, WinMM: %v", err0, err1)
}

func (c *context) Suspend() error {
	if c.wasapiContext != nil {
		return c.wasapiContext.Suspend()
	}
	if c.winmmContext != nil {
		return c.winmmContext.Suspend()
	}
	return nil
}

func (c *context) Resume() error {
	if c.wasapiContext != nil {
		return c.wasapiContext.Resume()
	}
	if c.winmmContext != nil {
		return c.winmmContext.Resume()
	}
	return nil
}

func (c *context) Err() error {
	if c.wasapiContext != nil {
		return c.wasapiContext.Err()
	}
	if c.winmmContext != nil {
		return c.winmmContext.Err()
	}
	return nil
}
