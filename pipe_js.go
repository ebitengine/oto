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
// This is basically same as io.Pipe, but is implemented in more effient way under the assumption that
// this works on a single thread environment so that locks are not required.
func pipe() (io.ReadCloser, io.WriteCloser) {
	w := &pipeWriter{}
	r := &pipeReader{w: w}
	return r, w
}

type pipeReader struct {
	w      *pipeWriter
	closed bool
}

func (r *pipeReader) Read(buf []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.w.closed && len(r.w.buf) == 0 {
		return 0, io.EOF
	}
	// If this returns 0 with no errors, the caller might block forever on browsers.
	// For example, bufio.Reader tries to Read until any byte can be read, but context switch never happens on browsers.
	for len(r.w.buf) == 0 {
		if r.closed {
			return 0, io.ErrClosedPipe
		}
		if r.w.closed && len(r.w.buf) == 0 {
			return 0, io.EOF
		}
		runtime.Gosched()
	}
	n := copy(buf, r.w.buf)
	r.w.buf = r.w.buf[n:]
	return n, nil
}

func (r *pipeReader) Close() error {
	r.closed = true
	return nil
}

type pipeWriter struct {
	buf    []byte
	closed bool
}

func (w *pipeWriter) Write(buf []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	for len(w.buf) >= pipeBufSize {
		if w.closed {
			return 0, io.ErrClosedPipe
		}
		runtime.Gosched()
	}
	w.buf = append(w.buf, buf...)
	return len(buf), nil
}

func (w *pipeWriter) Close() error {
	w.closed = true
	return nil
}
