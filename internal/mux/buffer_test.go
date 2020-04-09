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
	"time"

	"github.com/hajimehoshi/oto/internal/mux"
)

const concurrency = 1000

func TestBufferConcurrentWrites(t *testing.T) {
	b := mux.NewConcurrentBuffer()

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
		t.Errorf("Expected Len to be %v, but it was %v", concurrency, l)
	}

	out := make([]byte, concurrency)
	n, err := b.Read(out)
	if err != nil {
		t.Errorf("Expected no error from Read, but it returned: %v", err)
	}
	if n != concurrency {
		t.Errorf("Expected Read to return %v bytes, but it returned %v", concurrency, n)
	}

	for _, b := range out {
		if b != 1 {
			t.Errorf("Expected to find all 1s in the buffer, but there was a '%v'", b)
			break
		}
	}
}

func TestConcurrentReadWrites(t *testing.T) {
	b := mux.NewConcurrentBuffer()
	ch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		<-ch
		for i := 0; i < concurrency; i++ {
			b.Write([]byte{1})
		}
		wg.Done()
	}()
	timeout := make(chan struct{})
	go func() {
		time.Sleep(time.Second)
		timeout <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		<-ch
		buf := make([]byte, 1)
		i := 0
		for {
			select {
			case <-timeout:
				t.Errorf("Reached timeout while waiting for more data. Expected to receive %v bytes, but only received %v.", concurrency, i)
				return
			default:
				n, err := b.Read(buf)
				if err != nil && err != io.EOF {
					t.Errorf("Expected only nil and io.EOF errors from Read, but it returned: %v", err)
				}
				i = i + n
				if i == concurrency {
					return
				}
			}
		}
	}()
	close(ch)
	wg.Wait()
}
