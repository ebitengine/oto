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
	"os"
	"path/filepath"
	"sync"

	"github.com/jfreymuth/pulse"

	"github.com/ebitengine/oto/v3/internal/mux"
)

var newPulseAudioContextFunc pulseAudioContextFactory = newPulseAudioContext

type context struct {
	*pulseOnlyContext
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, clientApplicationName string) (*context, chan struct{}, error) {
	ctx, ready, err := newPulseOnlyContext(newPulseAudioContextFunc, sampleRate, channelCount, format, bufferSizeInBytes, clientApplicationName)
	if err != nil {
		return nil, nil, err
	}
	return &context{pulseOnlyContext: ctx}, ready, nil
}

type pulseAudioContextFactory func(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int, clientApplicationName string) (*pulseAudioContext, error)

type pulseAudioContext struct {
	client *pulse.Client
	stream *pulse.PlaybackStream

	suspended bool
	cond      *sync.Cond

	mux *mux.Mux
	err atomicError
}

func newPulseAudioContext(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int, applicationName string) (*pulseAudioContext, error) {
	c := &pulseAudioContext{
		cond: sync.NewCond(&sync.Mutex{}),
		mux:  mux,
	}

	if applicationName == "" {
		if name, _ := os.Executable(); name != "" {
			applicationName = filepath.Base(name)
		} else {
			applicationName = "Oto"
		}
	}

	client, err := pulse.NewClient(pulse.ClientApplicationName(applicationName))
	if err != nil {
		return nil, fmt.Errorf("oto: PulseAudio client initialization failed: %w", err)
	}
	c.client = client

	options := []pulse.PlaybackOption{
		pulse.PlaybackMediaName(applicationName),
	}
	switch channelCount {
	case 1:
		options = append(options, pulse.PlaybackMono)
	case 2:
		options = append(options, pulse.PlaybackStereo)
	default:
		client.Close()
		return nil, fmt.Errorf("oto: PulseAudio backend supports only mono or stereo output: %d", channelCount)
	}
	options = append(options, pulse.PlaybackSampleRate(sampleRate))
	if bufferSizeInBytes != 0 {
		latency := float64(bufferSizeInBytes) / float64(sampleRate*channelCount*4)
		if latency > 0 {
			options = append(options, pulse.PlaybackLatency(latency))
		}
	}

	stream, err := client.NewPlayback(pulse.Float32Reader(c.read), options...)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("oto: PulseAudio playback initialization failed: %w", err)
	}
	c.stream = stream
	c.stream.Start()

	return c, nil
}

func (c *pulseAudioContext) read(buf []float32) (int, error) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.suspended && c.err.Load() == nil {
		c.cond.Wait()
	}
	if err := c.err.Load(); err != nil {
		return 0, err
	}

	c.mux.ReadFloat32s(buf)
	return len(buf), nil
}

func (c *pulseAudioContext) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}

	c.suspended = true
	c.stream.Pause()
	return nil
}

func (c *pulseAudioContext) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}

	c.suspended = false
	c.stream.Resume()
	c.cond.Broadcast()
	return nil
}

func (c *pulseAudioContext) Err() error {
	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}
	return nil
}

type pulseOnlyContext struct {
	pulseAudioContext *pulseAudioContext

	ready chan struct{}
	err   atomicError

	mux *mux.Mux
}

func newPulseOnlyContext(factory pulseAudioContextFactory, sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, clientApplicationName string) (*pulseOnlyContext, chan struct{}, error) {
	ctx := &pulseOnlyContext{
		ready: make(chan struct{}),
		mux:   mux.New(sampleRate, channelCount, format),
	}

	go func() {
		defer close(ctx.ready)

		pc, err := factory(sampleRate, channelCount, ctx.mux, bufferSizeInBytes, clientApplicationName)
		if err != nil {
			ctx.err.TryStore(err)
			return
		}
		ctx.pulseAudioContext = pc
	}()

	return ctx, ctx.ready, nil
}

func (c *pulseOnlyContext) Suspend() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Suspend()
	}
	return nil
}

func (c *pulseOnlyContext) Resume() error {
	<-c.ready
	if c.pulseAudioContext != nil {
		return c.pulseAudioContext.Resume()
	}
	return nil
}

func (c *pulseOnlyContext) Err() error {
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
