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

package mux

import "time"

// paceRate is how many times faster than real time paced reads may come, so a
// driver catches up after a device takes a large burst at once.
const paceRate = 2

// A Pacer spaces a driver's reads from a Mux, so that each player refills its
// buffer from its source between them. A player hands over only what it has
// buffered, and the rest of a read is silence. Reads in quick succession, as a
// driver makes when a device takes a large burst at once, would otherwise ask a
// player for more than a small buffer holds.
//
// A Pacer is for one reader at a time.
type Pacer struct {
	mux  *Mux
	next time.Time
}

// NewPacer returns a Pacer reading from m.
func NewPacer(m *Mux) *Pacer {
	return &Pacer{mux: m}
}

// ReadFloat32s waits until the read is due, and reads buf from the mux, as
// Mux.ReadFloat32s does. A read is due once the last read's length, divided by
// paceRate, has passed since it was made.
func (p *Pacer) ReadFloat32s(buf []float32) {
	now := time.Now()
	if wait := p.next.Sub(now); wait > 0 {
		time.Sleep(wait)
		now = time.Now()
	}
	p.mux.ReadFloat32s(buf)
	frames := len(buf) / p.mux.channelCount
	p.next = now.Add(time.Duration(frames) * time.Second / time.Duration(p.mux.sampleRate) / paceRate)
}
