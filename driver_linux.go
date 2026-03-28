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
	"fmt"

	"github.com/ebitengine/oto/v3/internal/mux"
)

var (
	newPulseAudioContextFunc = newPulseAudioContext
	newALSAContextFunc       = newALSAContext
)

type context struct {
	pulseAudioContext *pulseAudioContext
	alsaContext       *alsaContext

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

		ac, err0 := newALSAContextFunc(sampleRate, channelCount, ctx.mux, bufferSizeInBytes)
		if err0 == nil {
			ctx.alsaContext = ac
			return
		}

		pc, err1 := newPulseAudioContextFunc(sampleRate, channelCount, ctx.mux, bufferSizeInBytes, clientApplicationName)
		if err1 == nil {
			ctx.pulseAudioContext = pc
			return
		}

		ctx.err.TryStore(fmt.Errorf("oto: initialization failed: ALSA: %v, PulseAudio: %v", err0, err1))
	}()

	return ctx, ctx.ready, nil
}

func (c *context) Suspend() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Suspend()
	}
	if c.alsaContext != nil {
		return c.alsaContext.Suspend()
	}
	return nil
}

func (c *context) Resume() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Resume()
	}
	if c.alsaContext != nil {
		return c.alsaContext.Resume()
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
	if c.alsaContext != nil {
		return c.alsaContext.Err()
	}
	return nil
}
