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
	"io"
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
