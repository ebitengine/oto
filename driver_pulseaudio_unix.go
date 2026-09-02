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

type pulseContext struct {
	client *pulse.Client
	stream *pulse.PlaybackStream

	suspended bool
	cond      *sync.Cond

	// suspendMu serializes Suspend and Resume, which are concurrent-safe, so that the
	// stream ends up in the state that the last of them requested.
	suspendMu sync.Mutex

	mux *mux.Mux
	err atomicError
}

func newPulseContext(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int, applicationName string) (*pulseContext, error) {
	c := &pulseContext{
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
		c.client.Close()
		return nil, fmt.Errorf("oto: PulseAudio backend supports only mono or stereo output: %d", channelCount)
	}
	options = append(options, pulse.PlaybackSampleRate(sampleRate))
	{
		latency := float64(bufferSizeInBytes) / float64(sampleRate*channelCount*4)
		if latency <= 0 {
			// If no buffer size is specified, default to a 100ms latency.
			// Without this, PulseAudio uses its own large default buffer (~2s),
			// which causes a noticeable delay before audio starts playing.
			latency = 0.1
		}
		options = append(options, pulse.PlaybackLatency(latency))
	}

	stream, err := c.client.NewPlayback(pulse.Float32Reader(c.read), options...)
	if err != nil {
		c.client.Close()
		return nil, fmt.Errorf("oto: PulseAudio playback initialization failed: %w", err)
	}
	c.stream = stream
	c.stream.Start()

	return c, nil
}

func (c *pulseContext) read(buf []float32) (int, error) {
	if err := c.waitUntilResumed(); err != nil {
		return 0, err
	}

	c.mux.ReadFloat32s(buf)
	return len(buf), nil
}

// waitUntilResumed blocks while the context is suspended.
func (c *pulseContext) waitUntilResumed() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.suspended && c.err.Load() == nil {
		c.cond.Wait()
	}
	return c.err.Load()
}

// setSuspended updates the suspended state and wakes up a waiting reader.
func (c *pulseContext) setSuspended(suspended bool) error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}

	c.suspended = suspended
	if !suspended {
		c.cond.Signal()
	}
	return nil
}

func (c *pulseContext) Suspend() error {
	c.suspendMu.Lock()
	defer c.suspendMu.Unlock()

	if err := c.setSuspended(true); err != nil {
		return err
	}

	// Cork the stream without holding c.cond.L. Corking waits for a reply that is read by
	// the same goroutine that dispatches buffer requests to the reader, and the reader
	// might be blocked on c.cond.L.
	c.stream.Pause()
	return nil
}

func (c *pulseContext) Resume() error {
	c.suspendMu.Lock()
	defer c.suspendMu.Unlock()

	// Wake up the reader before uncorking. The reader must be able to return so that the
	// goroutine dispatching buffer requests can accept a new one, otherwise the reply to
	// uncorking is never read.
	if err := c.setSuspended(false); err != nil {
		return err
	}

	c.stream.Resume()
	return nil
}

func (c *pulseContext) Err() error {
	if err := c.err.Load(); err != nil {
		return err
	}
	if err := c.stream.Error(); err != nil {
		return fmt.Errorf("oto: PulseAudio error: %w", err)
	}
	return nil
}
