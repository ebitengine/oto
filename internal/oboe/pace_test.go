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

package oboe_test

import (
	"encoding/binary"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
	"github.com/ebitengine/oto/v3/internal/oboe"
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

// played is what a run of readLikeAndroid heard after its warm-up.
type played struct {
	// silent and total count the samples read from the mux, and short the
	// times the device found fewer frames queued than its burst.
	silent, total, short int
}

// readLikeAndroid plays m as the Android driver reads it for a Bluetooth
// headset, for d: the device takes a burst of 1,920 frames every 40 ms, and
// read tops a queue of two bursts and a read up, 480 frames at a time, as soon
// as there is room. It counts what is heard after the first fifth of d.
func readLikeAndroid(read func([]float32), d time.Duration) played {
	const (
		rate   = 48000
		burst  = 1920
		chunk  = 480
		target = 2*burst + chunk
	)
	var queued atomic.Int64
	var short atomic.Int64
	start := time.Now()
	warm := func() bool { return time.Since(start) >= d/5 }

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second * burst / rate)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			// The device takes its burst, or what is queued, without losing a
			// reader's frames added meanwhile.
			for {
				q := queued.Load()
				take := min(q, burst)
				if queued.CompareAndSwap(q, q-take) {
					if take < burst && warm() {
						short.Add(1)
					}
					break
				}
			}
		}
	}()

	var p played
	buf := make([]float32, chunk*2)
	for time.Since(start) < d {
		if queued.Load()+chunk > target {
			time.Sleep(time.Millisecond)
			continue
		}
		read(buf)
		queued.Add(chunk)
		if !warm() {
			continue
		}
		for _, v := range buf {
			if v == 0 {
				p.silent++
			}
		}
		p.total += len(buf)
	}
	close(stop)
	<-done
	p.short = int(short.Load())
	return p
}

// newWorkingMux plays a source of sound on a mux at 48 kHz, through a player
// buffering 20 ms, once its buffer is full.
func newWorkingMux(t *testing.T) *mux.Mux {
	t.Helper()

	m := mux.New(48000, 2, mux.FormatFloat32LE)
	// The mux's loop must end before the synctest bubble does.
	t.Cleanup(m.Stop)
	p := m.NewPlayer(&workingReader{work: 2 * time.Millisecond})
	t.Cleanup(func() {
		_ = p.Close()
	})
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
//
// The test runs in a synctest bubble, whose time is virtual, so its result
// depends on no machine's scheduler.
func TestPacedReadsKeepASmallPlayerBufferPlaying(t *testing.T) {
	synctest.Test(t, testPacedReads)
}

func testPacedReads(t *testing.T) {
	// Read as soon as there is room, a player buffering 20 ms runs dry: the
	// reads after a burst come faster than it refills. This shows the test
	// meets the case.
	m := newWorkingMux(t)
	if got := readLikeAndroid(m.ReadFloat32s, time.Second); got.silent == 0 {
		t.Fatalf("unpaced reads: %d of %d samples silent; want some, as the player runs dry", got.silent, got.total)
	}

	m = newWorkingMux(t)
	p := oboe.NewPacer(m.ReadFloat32s, 48000, 2)
	got := readLikeAndroid(p.Read, time.Second)
	if got.silent != 0 || got.total == 0 {
		t.Errorf("paced reads: %d of %d samples silent; want none", got.silent, got.total)
	}
	if got.short != 0 {
		t.Errorf("paced reads: the device found too little queued %d times; want none", got.short)
	}
}
