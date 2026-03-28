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

//go:build linux && !android

package oto

import (
	"fmt"
	"sync"

	"github.com/ebitengine/oto/v3/internal/mux"
	"github.com/jfreymuth/pulse"
)

type pulseAudioContext struct {
	client *pulse.Client
	stream *pulse.PlaybackStream

	suspended bool
	cond      *sync.Cond

	mux *mux.Mux
	err atomicError
}

func newPulseAudioContext(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (*pulseAudioContext, error) {
	c := &pulseAudioContext{
		cond: sync.NewCond(&sync.Mutex{}),
		mux:  mux,
	}

	client, err := pulse.NewClient(pulse.ClientApplicationName("Oto"))
	if err != nil {
		return nil, fmt.Errorf("oto: PulseAudio client initialization failed: %w", err)
	}
	c.client = client

	options := []pulse.PlaybackOption{
		pulse.PlaybackMediaName("Oto"),
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
