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

package mux_test

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
)

// blockingReader is a source that never returns from Read until unblock is closed.
type blockingReader struct {
	unblock chan struct{}
}

func (r *blockingReader) Read(buf []byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}

func (r *blockingReader) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}

func newBlockingPlayer(t *testing.T) *mux.Player {
	t.Helper()

	r := &blockingReader{
		unblock: make(chan struct{}),
	}
	t.Cleanup(func() {
		close(r.unblock)
	})
	return mux.New(48000, 2, mux.FormatSignedInt16LE).NewPlayer(r)
}

// Issue #270
func TestPlayDoesNotBlockOnSource(t *testing.T) {
	p := newBlockingPlayer(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Play()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Play blocked on reading the source")
	}
}

// Issue #270
func TestBufferIsFilledAfterPlay(t *testing.T) {
	const bufferSize = 4096

	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(bytes.NewReader(make([]byte, 2*bufferSize)))
	p.SetBufferSize(bufferSize)
	p.Play()

	deadline := time.Now().Add(time.Second)
	for p.BufferedSize() < bufferSize {
		if time.Now().After(deadline) {
			t.Fatalf("the buffer was not filled: got %d; want %d", p.BufferedSize(), bufferSize)
		}
		time.Sleep(time.Millisecond)
	}
}

// gatedReader is a source whose Read does not return until gate is closed.
// Read fails after Close is called.
type gatedReader struct {
	gate      chan struct{}
	began     chan struct{}
	beganOnce sync.Once
	reads     atomic.Int64

	m      sync.Mutex
	closed bool
}

func (r *gatedReader) Read(buf []byte) (int, error) {
	r.reads.Add(1)
	r.beganOnce.Do(func() {
		close(r.began)
	})
	<-r.gate

	r.m.Lock()
	defer r.m.Unlock()
	if r.closed {
		return 0, errors.New("gatedReader: read after close")
	}
	for i := range buf {
		buf[i] = 0
	}
	return len(buf), nil
}

func (r *gatedReader) Close() error {
	r.m.Lock()
	defer r.m.Unlock()
	r.closed = true
	return nil
}

// Issue #288
func TestResetStopsReadingSource(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		p.Reset()
	}()

	select {
	case <-resetDone:
		t.Fatal("Reset returned while a read from the source was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the ongoing read finish. Reset should return soon.
	close(src.gate)
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not return after the ongoing read finished")
	}

	if got := p.BufferedSize(); got != 0 {
		t.Errorf("BufferedSize after Reset: got %d; want 0", got)
	}

	// After Reset, the source must not be read until Play is called.
	// Consume the mux's buffer to tempt it to read the source again.
	reads := src.reads.Load()
	m.ReadFloat32s(make([]float32, 256))
	time.Sleep(100 * time.Millisecond)
	if got := src.reads.Load(); got != reads {
		t.Errorf("the source was read after Reset: got %d reads; want %d", got, reads)
	}
	if err := p.Err(); err != nil {
		t.Errorf("Err after Reset: got %v; want nil", err)
	}

	// The player must be reusable after Reset.
	p.Play()
	deadline := time.Now().Add(time.Second)
	for src.reads.Load() == reads {
		if time.Now().After(deadline) {
			t.Error("the source was not read after Play following Reset")
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// Issue #288
func TestClosingSourceWithoutResetCausesError(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	// Pause does not stop the in-flight read.
	p.Pause()
	_ = src.Close()
	close(src.gate)

	// The in-flight read fails as the source is already closed, and the player gets the error.
	deadline := time.Now().Add(time.Second)
	for p.Err() == nil {
		if time.Now().After(deadline) {
			t.Error("Err did not return an error after the source was closed without Reset")
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// Issue #288
func TestClosingSourceAfterResetIsSafe(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		p.Reset()
	}()

	// Let the ongoing read finish so that Reset can return.
	close(src.gate)
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not return after the ongoing read finished")
	}

	_ = src.Close()
	reads := src.reads.Load()

	// Consume the mux's buffer to tempt it to read the source again.
	m.ReadFloat32s(make([]float32, 256))
	time.Sleep(100 * time.Millisecond)

	if got := src.reads.Load(); got != reads {
		t.Errorf("the source was read after Reset: got %d reads; want %d", got, reads)
	}
	if err := p.Err(); err != nil {
		t.Errorf("Err after closing the source following Reset: got %v; want nil", err)
	}
}

// Issue #270
func TestSeekDoesNotBlockOnSource(t *testing.T) {
	p := newBlockingPlayer(t)
	p.Play()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Seek(0, io.SeekStart)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Seek blocked on reading the source")
	}
}
