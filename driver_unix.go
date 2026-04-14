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

type context struct {
	client *pulse.Client
	stream *pulse.PlaybackStream

	suspended bool
	cond      *sync.Cond

	mux *mux.Mux
	err atomicError
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, applicationName string) (client *context, ready chan struct{}, err error) {
	client = &context{
		cond: sync.NewCond(&sync.Mutex{}),
		mux:  mux.New(sampleRate, channelCount, format),
	}
	ready = make(chan struct{})
	close(ready)
	defer func() {
		if client != nil && client.client != nil && err != nil {
			client.client.Close()
		}
	}()

	if applicationName == "" {
		if name, _ := os.Executable(); name != "" {
			applicationName = filepath.Base(name)
		} else {
			applicationName = "Oto"
		}
	}

	client.client, err = pulse.NewClient(pulse.ClientApplicationName(applicationName))
	if err != nil {
		return nil, ready, fmt.Errorf("oto: PulseAudio client initialization failed: %w", err)
	}

	options := []pulse.PlaybackOption{
		pulse.PlaybackMediaName(applicationName),
	}
	switch channelCount {
	case 1:
		options = append(options, pulse.PlaybackMono)
	case 2:
		options = append(options, pulse.PlaybackStereo)
	default:
		return nil, ready, fmt.Errorf("oto: PulseAudio backend supports only mono or stereo output: %d", channelCount)
	}
	options = append(options, pulse.PlaybackSampleRate(sampleRate))
	if bufferSizeInBytes != 0 {
		latency := float64(bufferSizeInBytes) / float64(sampleRate*channelCount*4)
		if latency > 0 {
			options = append(options, pulse.PlaybackLatency(latency))
		}
	}

	client.stream, err = client.client.NewPlayback(pulse.Float32Reader(client.read), options...)
	if err != nil {
		return nil, ready, fmt.Errorf("oto: PulseAudio playback initialization failed: %w", err)
	}
	client.stream.Start()

	return client, ready, nil
}

func (c *context) read(buf []float32) (int, error) {
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

func (c *context) Suspend() error {
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

func (c *context) Resume() error {
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
	c.cond.Signal()
	return nil
}

func (c *context) Err() error {
	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}
	return nil
}
