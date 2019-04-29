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

// +build js

package oto

import (
	"io"
	"runtime"
)

const pipeBufSize = 4096

// pipe returns a set of an io.ReadCloser and an io.WriteCloser.
//
// This is basically same as io.Pipe, but is implemented in more effient way under these assumptions:
// 1) These works on a single thread environment so that locks are not required.
// 2) Reading and writing happen equally.
// 3) Closing doesn't have to do anything special.
func pipe() (io.ReadCloser, io.WriteCloser) {
	w := &pipeWriter{}
	r := &pipeReader{w: w}
	return r, w
}

type pipeReader struct {
	w *pipeWriter
}

func (r *pipeReader) Read(buf []byte) (int, error) {
	// If this returns 0, the caller might block forever on browsers.
	// For example, bufio.Reader tries to Read until any byte can be read, but context switch never happens on browsers.
	for len(r.w.buf) == 0 {
		runtime.Gosched()
	}
	n := copy(buf, r.w.buf)
	r.w.buf = r.w.buf[n:]
	return n, nil
}

func (r *pipeReader) Close() error {
	return nil
}

type pipeWriter struct {
	buf []byte
}

func (w *pipeWriter) Write(buf []byte) (int, error) {
	for len(w.buf) >= pipeBufSize {
		runtime.Gosched()
	}
	w.buf = append(w.buf, buf...)
	return len(buf), nil
}

func (w *pipeWriter) Close() error {
	return nil
}
