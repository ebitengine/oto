// Copyright 2020 The Oto Authors
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
	"io"
	"sync"
	"testing"

	"github.com/hajimehoshi/oto/internal/mux"
)

const concurrency = 1000

func TestBufferWriteBlocksOnRead(t *testing.T) {
	t.Fatal("todo")
}

func TestBufferConcurrentWrites(t *testing.T) {
	t.Fatal("todo")

	b := &mux.ConcurrentBuffer{}

	var done, start sync.WaitGroup
	done.Add(concurrency)
	start.Add(1)
	for i := 0; i < concurrency; i++ {
		go func() {
			start.Wait()
			b.Write([]byte{1})
			done.Done()
		}()
	}
	start.Done()
	done.Wait()

	if l := b.Len(); l != concurrency {
		t.Errorf("b.Len: got: %v, want: %v", l, concurrency)
	}

	out := make([]byte, concurrency)
	if _, err := io.ReadFull(b, out); err != nil {
		t.Fatal(err)
	}

	for _, b := range out {
		if b != 1 {
			t.Errorf("expected to find all 1s in the buffer, but there was a '%v'", b)
			break
		}
	}
}

func TestConcurrentReadWrites(t *testing.T) {
	b := mux.ConcurrentBuffer{}
	done := make(chan struct{})

	go func() {
		for i := 0; i < concurrency; i++ {
			b.Write([]byte{1})
		}
		close(done)
	}()

	bytesRead := 0
	for {
		lastRead := false
		select {
		case <-done:
			lastRead = true
		default:
		}

		buf := make([]byte, concurrency)
		n, err := b.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("b.Read error: got: %v want: %v or %v", err, nil, io.EOF)
		}
		bytesRead += n

		if lastRead {
			if bytesRead != concurrency {
				t.Errorf("total bytes read: got: %v want: %v", bytesRead, concurrency)
			}
			break
		}
	}
}
