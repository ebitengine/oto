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

func TestBufferConcurrentWrites(t *testing.T) {
	b := &mux.ConcurrentBuffer{}

	ch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			<-ch
			b.Write([]byte{1})
			wg.Done()
		}()
	}
	close(ch)
	wg.Wait()

	if l := b.Len(); l != concurrency {
		t.Errorf("b.Len: got: %v, want: %v", l, concurrency)
	}

	out := make([]byte, concurrency)
	if _, err := io.ReadFull(b, out); err != nil {
		t.Fatal(err)
	}

	for _, b := range out {
		if b != 1 {
			t.Errorf("Expected to find all 1s in the buffer, but there was a '%v'", b)
			break
		}
	}
}

func TestConcurrentReadWrites(t *testing.T) {
	b := mux.ConcurrentBuffer{}
	ch := make(chan struct{})
	doneWriting := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()

		<-ch
		for i := 0; i < concurrency; i++ {
			b.Write([]byte{1})
		}
		doneWriting <- struct{}{}
	}()
	go func() {
		defer wg.Done()

		<-ch

		bytesRead := 0
		for {
			lastRead := false
			select {
			case <-doneWriting:
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
	}()
	close(ch)
	wg.Wait()
}
