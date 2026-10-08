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

package oboe

import "time"

// paceRate is how many times faster than real time paced reads may come, so
// the driver catches up after a device takes a large burst at once.
const paceRate = 2

// A pacer spaces reads, so that they come at most paceRate times faster than
// real time. It is for one reader at a time.
type pacer struct {
	read         func(buf []float32)
	sampleRate   int
	channelCount int
	next         time.Time
}

// newPacer returns a pacer reading with read, from a source of sampleRate
// frames a second and channelCount channels.
func newPacer(read func(buf []float32), sampleRate, channelCount int) *pacer {
	return &pacer{
		read:         read,
		sampleRate:   sampleRate,
		channelCount: channelCount,
	}
}

// Read waits until the read is due, and reads buf. A read is due once the last
// read's length, divided by paceRate, has passed since it was made.
func (p *pacer) Read(buf []float32) {
	now := time.Now()
	if wait := p.next.Sub(now); wait > 0 {
		time.Sleep(wait)
		now = time.Now()
	}
	p.read(buf)
	frames := len(buf) / p.channelCount
	p.next = now.Add(time.Duration(frames) * time.Second / time.Duration(p.sampleRate) / paceRate)
}
