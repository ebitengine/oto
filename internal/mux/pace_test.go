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
	"encoding/binary"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
)

// workingReader is a source that always has sound, a sample of 1 after
// another, and takes a while to make each read, as a decoder does.
type workingReader struct {
	work time.Duration
}

func (r *workingReader) Read(buf []byte) (int, error) {
	time.Sleep(r.work)
	n := len(buf) / 4 * 4
	for i := 0; i < n; i += 4 {
		binary.LittleEndian.PutUint32(buf[i:], math.Float32bits(1))
	}
	return n, nil
}

// readLikeAndroid plays m as the Android driver reads it for a Bluetooth
// headset, for d: the device takes a burst of 1,920 frames every 40 ms, and
// read tops a queue of 3,840 frames up, 480 frames at a time, as soon as there
// is room. It returns how many of the samples read after the first fifth of d
// were silent, and how many there were.
func readLikeAndroid(m *mux.Mux, read func([]float32), d time.Duration) (silent, total int) {
	const (
		rate   = 48000
		burst  = 1920
		target = 3840
		chunk  = 480
	)
	var queued atomic.Int64
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second * burst / rate)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				queued.Store(max(queued.Load()-burst, 0))
			}
		}
	}()
	defer close(stop)

	buf := make([]float32, chunk*2)
	start := time.Now()
	for time.Since(start) < d {
		if queued.Load()+chunk > target {
			time.Sleep(time.Millisecond)
			continue
		}
		read(buf)
		queued.Add(chunk)
		if time.Since(start) < d/5 {
			continue
		}
		for _, v := range buf {
			if v == 0 {
				silent++
			}
		}
		total += len(buf)
	}
	return silent, total
}

// newWorkingPlayer plays a source of sound on a mux at 48 kHz, through a
// player buffering 20 ms, once its buffer is full.
func newWorkingPlayer(t *testing.T) *mux.Mux {
	t.Helper()

	m := mux.New(48000, 2, mux.FormatFloat32LE)
	p := newPlayer(t, m, &workingReader{work: 2 * time.Millisecond})
	const bufferSize = 48000 * 8 / 50
	p.SetBufferSize(bufferSize)
	p.Play()
	deadline := time.Now().Add(time.Second)
	for p.BufferedSize() < bufferSize {
		if time.Now().After(deadline) {
			t.Fatal("the buffer was not filled")
		}
		time.Sleep(time.Millisecond)
	}
	return m
}

// Issue #308
func TestPacedReadsKeepASmallPlayerBufferPlaying(t *testing.T) {
	if testing.Short() {
		t.Skip("plays in real time")
	}

	// Read as soon as there is room, a player buffering 20 ms runs dry: the
	// reads after a burst come faster than it refills. This shows the test
	// meets the case.
	m := newWorkingPlayer(t)
	if silent, total := readLikeAndroid(m, m.ReadFloat32s, time.Second); silent == 0 {
		t.Fatalf("unpaced reads: %d of %d samples silent; want some, as the player runs dry", silent, total)
	}

	m = newWorkingPlayer(t)
	pacer := mux.NewPacer(m)
	if silent, total := readLikeAndroid(m, pacer.ReadFloat32s, time.Second); silent != 0 || total == 0 {
		t.Errorf("paced reads: %d of %d samples silent; want none", silent, total)
	}
}
