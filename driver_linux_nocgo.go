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

//go:build linux && !android && !cgo

package oto

import "github.com/ebitengine/oto/v3/internal/mux"

var newPulseAudioContextFunc = newPulseAudioContext

type context struct {
	pulseAudioContext *pulseAudioContext

	ready chan struct{}
	err   atomicError

	mux *mux.Mux
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, clientApplicationName string) (*context, chan struct{}, error) {
	ctx := &context{
		ready: make(chan struct{}),
		mux:   mux.New(sampleRate, channelCount, format),
	}

	go func() {
		defer close(ctx.ready)

		pc, err := newPulseAudioContextFunc(sampleRate, channelCount, ctx.mux, bufferSizeInBytes, clientApplicationName)
		if err != nil {
			ctx.err.TryStore(err)
			return
		}
		ctx.pulseAudioContext = pc
	}()

	return ctx, ctx.ready, nil
}

func (c *context) Suspend() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Suspend()
	}
	return nil
}

func (c *context) Resume() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Resume()
	}
	return nil
}

func (c *context) Err() error {
	if err := c.err.Load(); err != nil {
		return err
	}

	select {
	case <-c.ready:
	default:
		return nil
	}

	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Err()
	}
	return nil
}
