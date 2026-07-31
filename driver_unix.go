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

//go:build !android && !darwin && !js && !windows && !nintendosdk && !playstation5

package oto

import (
	"fmt"

	"github.com/ebitengine/oto/v3/internal/mux"
)

// unixBackend is the part of a context that talks to the actual audio device.
type unixBackend interface {
	Suspend() error
	Resume() error
	Err() error
}

// newALSAContext creates the ALSA fallback backend. driver_alsa_unix.go's init sets it on the
// platforms where that file is built; it is nil elsewhere (e.g. OpenBSD), in which case only the
// PulseAudio backend is attempted.
var newALSAContext func(sampleRate, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (unixBackend, error)

type context struct {
	mux     *mux.Mux
	backend unixBackend

	ready chan struct{}
	err   atomicError
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, applicationName string) (*context, chan struct{}, error) {
	ctx := &context{
		mux:   mux.New(sampleRate, channelCount, format),
		ready: make(chan struct{}),
	}

	// Initializing a driver might take some time, so do it asynchronously.
	// PulseAudio is the default; if no server is reachable, fall back to ALSA.
	go func() {
		defer close(ctx.ready)

		pc, err0 := newPulseContext(sampleRate, channelCount, ctx.mux, bufferSizeInBytes, applicationName)
		if err0 == nil {
			ctx.backend = pc
			return
		}

		if newALSAContext == nil {
			ctx.err.Join(err0)
			return
		}

		ac, err1 := newALSAContext(sampleRate, channelCount, ctx.mux, bufferSizeInBytes)
		if err1 == nil {
			ctx.backend = ac
			return
		}

		ctx.err.Join(fmt.Errorf("oto: initialization failed: PulseAudio: %w; ALSA: %w", err0, err1))
	}()

	return ctx, ctx.ready, nil
}

func (c *context) Suspend() error {
	<-c.ready
	if c.backend != nil {
		return c.backend.Suspend()
	}
	return nil
}

func (c *context) Resume() error {
	<-c.ready
	if c.backend != nil {
		return c.backend.Resume()
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

	if c.backend != nil {
		return c.backend.Err()
	}
	return nil
}
