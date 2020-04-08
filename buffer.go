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

package oto

import (
	"bytes"
	"sync"
)

type concurrentBuffer struct {
	buf *bytes.Buffer
	m   *sync.Mutex
}

func (b concurrentBuffer) Len() int {
	b.m.Lock()
	defer b.m.Unlock()

	return b.buf.Len()
}

func (b concurrentBuffer) Read(buf []byte) (int, error) {
	b.m.Lock()
	defer b.m.Unlock()

	return b.buf.Read(buf)
}

func (b concurrentBuffer) Write(buf []byte) (int, error) {
	b.m.Lock()
	defer b.m.Unlock()

	return b.buf.Write(buf)
}
