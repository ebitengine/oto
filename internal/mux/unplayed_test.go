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
	"bytes"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
)

// The tests play 48 kHz stereo float32: 8 bytes a frame.
const bytesPerFrame = 8

// newDelayedMux returns a mux whose driver reports delay frames as not heard
// yet, where known is true.
//
// The tests run in synctest bubbles, and wait for the mux's loop to fill the
// players' buffers with synctest.Wait, so their results depend on no
// machine's scheduler.
func newDelayedMux(t *testing.T, delay int64, known bool) *mux.Mux {
	t.Helper()

	m := mux.New(48000, 2, mux.FormatFloat32LE)
	// The mux's loop must end before the synctest bubble does. It sees Stop
	// the next time it waits, which can be after it sleeps a moment, as it
	// does once a source has ended; in the bubble the sleep takes no time.
	t.Cleanup(func() {
		m.Stop()
		time.Sleep(time.Second)
	})
	if known {
		m.SetDelayFunc(func() (int64, bool) {
			return delay, true
		})
	}
	return m
}

// newBufferedPlayer plays frames frames of sound on m, once the player has
// read all of them from its source. It is called in a synctest bubble.
func newBufferedPlayer(t *testing.T, m *mux.Mux, frames int) *mux.Player {
	t.Helper()

	p := newPlayer(t, m, bytes.NewReader(bytes.Repeat([]byte{0, 0, 0x80, 0x3f}, frames*2)))
	p.SetBufferSize(frames * bytesPerFrame)
	p.Play()
	synctest.Wait()
	if got, want := p.BufferedSize(), frames*bytesPerFrame; got != want {
		t.Fatalf("the player buffered %d bytes; want %d", got, want)
	}
	return p
}

// readFrames mixes frames frames from m, as a driver reads.
func readFrames(m *mux.Mux, frames int) {
	m.ReadFloat32s(make([]float32, frames*2))
}

func TestUnplayedSizeIsBufferedSizeWithoutADelay(t *testing.T) {
	synctest.Test(t, testUnplayedSizeIsBufferedSizeWithoutADelay)
}

func testUnplayedSizeIsBufferedSizeWithoutADelay(t *testing.T) {
	m := newDelayedMux(t, 0, false)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, 480)
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeCountsWhatIsNotHeardYet(t *testing.T) {
	synctest.Test(t, testUnplayedSizeCountsWhatIsNotHeardYet)
}

func testUnplayedSizeCountsWhatIsNotHeardYet(t *testing.T) {
	const delay = 1000
	m := newDelayedMux(t, delay, true)
	p := newBufferedPlayer(t, m, 4800)

	// Right after Play, nothing is sent yet.
	if got, want := p.UnplayedSize(), 4800*bytesPerFrame; got != want {
		t.Fatalf("before any read: UnplayedSize() = %d; want %d", got, want)
	}
	// While less than the delay is mixed, none of it is heard: the whole
	// source is unplayed.
	readFrames(m, 480)
	readFrames(m, 480)
	if got, want := p.UnplayedSize(), 4800*bytesPerFrame; got != want {
		t.Fatalf("after 960 frames: UnplayedSize() = %d; want %d", got, want)
	}
	// Then the delay's worth of what is sent stays unheard.
	readFrames(m, 480)
	if got, want := p.UnplayedSize(), p.BufferedSize()+delay*bytesPerFrame; got != want {
		t.Errorf("after 1,440 frames: UnplayedSize() = %d; want BufferedSize() and the delay, %d", got, want)
	}
}

func TestUnplayedSizeCountsWhatIsSentBeforePause(t *testing.T) {
	synctest.Test(t, testUnplayedSizeCountsWhatIsSentBeforePause)
}

func testUnplayedSizeCountsWhatIsSentBeforePause(t *testing.T) {
	const delay = 1000
	m := newDelayedMux(t, delay, true)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, 1440)
	p.Pause()

	// What was sent before Pause is still to be heard, and is heard as the
	// device plays on.
	if got, want := p.UnplayedSize(), p.BufferedSize()+delay*bytesPerFrame; got != want {
		t.Fatalf("right after Pause: UnplayedSize() = %d; want %d", got, want)
	}
	readFrames(m, 400)
	if got, want := p.UnplayedSize(), p.BufferedSize()+(delay-400)*bytesPerFrame; got != want {
		t.Fatalf("400 frames after Pause: UnplayedSize() = %d; want %d", got, want)
	}
	readFrames(m, 600)
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("a delay after Pause: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard(t *testing.T) {
	synctest.Test(t, testUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard)
}

func testUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard(t *testing.T) {
	const delay = 1000
	m := newDelayedMux(t, delay, true)
	p := newBufferedPlayer(t, m, 960)
	readFrames(m, 960)
	// The mux finds the source's end on its next read from it.
	synctest.Wait()
	if p.IsPlaying() {
		t.Fatal("the player plays on after its source is mixed")
	}
	if got, want := p.UnplayedSize(), 960*bytesPerFrame; got != want {
		t.Fatalf("as the source ends: UnplayedSize() = %d; want %d", got, want)
	}
	readFrames(m, 1000)
	if got := p.UnplayedSize(); got != 0 {
		t.Errorf("a delay after the source ends: UnplayedSize() = %d; want 0", got)
	}
}

func TestUnplayedSizeForgetsWhatIsSentBeforeSeek(t *testing.T) {
	synctest.Test(t, testUnplayedSizeForgetsWhatIsSentBeforeSeek)
}

func testUnplayedSizeForgetsWhatIsSentBeforeSeek(t *testing.T) {
	const delay = 1000
	m := newDelayedMux(t, delay, true)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, 1440)
	if _, err := p.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("after Seek: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}
