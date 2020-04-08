// Copyright 2019 The Oto Authors
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

// +build !js

package oto

import (
	"bytes"
	"io"
	"sync"

	"github.com/hajimehoshi/oto/internal/mux"
)

type concurrentBufferReader struct {
	buf *bytes.Buffer
	m   *sync.Mutex
}

func (r concurrentBufferReader) Len() int {
	r.m.Lock()
	defer r.m.Unlock()

	return r.buf.Len()
}

func (r concurrentBufferReader) Read(buf []byte) (int, error) {
	r.m.Lock()
	defer r.m.Unlock()

	return r.buf.Read(buf)
}

type concurrentBufferWriter struct {
	buf *bytes.Buffer
	m   *sync.Mutex
}

func (w concurrentBufferWriter) Write(buf []byte) (int, error) {
	w.m.Lock()
	defer w.m.Unlock()

	return w.buf.Write(buf)
}

func pipe() (mux.LenReader, io.Writer) {
	var buf bytes.Buffer
	var m sync.Mutex

	r := concurrentBufferReader{
		buf: &buf,
		m:   &m,
	}
	w := concurrentBufferWriter{
		buf: &buf,
		m:   &m,
	}
	return r, w
}
