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

	"github.com/ebitengine/oto/v3/internal/mux"
	"github.com/ebitengine/oto/v3/internal/oboe"
)

type context struct {
	mux *mux.Mux

	err atomicError

	m sync.Mutex
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, _ string) (*context, chan struct{}, error) {
	ready := make(chan struct{})

	c := &context{
		mux: mux.New(sampleRate, channelCount, format),
	}
	go func() {
		c.m.Lock()
		defer c.m.Unlock()

		if err := oboe.Play(sampleRate, channelCount, c.mux.ReadFloat32s, bufferSizeInBytes); err != nil {
			c.err.TryStore(err)
			return
		}
		close(ready)
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

func (c *context) Err() error {
	return c.err.Load()
}
