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
	"sync"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
	"github.com/ebitengine/oto/v3/internal/oboe"
)

type context struct {
	mux        *mux.Mux
	sampleRate int

	err atomicError

	m sync.Mutex
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, _ string) (*context, chan struct{}, error) {
	ready := make(chan struct{})

	c := &context{
		mux:        mux.New(sampleRate, channelCount, format),
		sampleRate: sampleRate,
	}
	go func() {
		// The ready channel must close even if Play fails, or callers waiting on it block forever.
		defer close(ready)

		c.m.Lock()
		defer c.m.Unlock()

		if err := oboe.Play(sampleRate, channelCount, c.mux.ReadFloat32s, c.err.Join, bufferSizeInBytes); err != nil {
			c.err.Join(err)
		}
	}()
	return c, ready, nil
}

func (c *context) Suspend() error {
	c.m.Lock()
	defer c.m.Unlock()
	return oboe.Suspend()
}

func (c *context) Resume() error {
	c.m.Lock()
	defer c.m.Unlock()
	return oboe.Resume()
}

func (c *context) outputLatency() (time.Duration, bool) {
	n, ok := oboe.Latency()
	if !ok {
		return 0, false
	}
	return time.Duration(n) * time.Second / time.Duration(c.sampleRate), true
}

func (c *context) Err() error {
	return c.err.Load()
}
