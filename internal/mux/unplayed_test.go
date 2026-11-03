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

// newMux returns a mux for the tests.
//
// The tests run in synctest bubbles, and wait for the mux's loop to fill the
// players' buffers with synctest.Wait, so their results depend on no
// machine's scheduler, and they move the clock the mux reads with time.Sleep.
func newMux(t *testing.T) *mux.Mux {
	t.Helper()

	m := mux.New(48000, 2, mux.FormatFloat32LE)
	// The mux's loop must end before the synctest bubble does. It sees Stop
	// the next time it waits, which can be after it sleeps a moment, as it
	// does once a source has ended; in the bubble the sleep takes no time.
	t.Cleanup(func() {
		m.Stop()
		time.Sleep(time.Second)
	})
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

// frameTime is how long frames frames take to play.
func frameTime(frames int) time.Duration {
	return time.Duration(frames) * time.Second / 48000
}

// The tests' driver reports that frame 1,440 of the mix is heard 960 frames,
// 20 ms, from now.
const (
	reportedFrame = 1440
	delay         = 960
)

func reportHeard(m *mux.Mux) {
	m.SetHeard(reportedFrame, time.Now().Add(frameTime(delay)))
}

func TestUnplayedSizeIsBufferedSizeWithoutAReport(t *testing.T) {
	synctest.Test(t, testUnplayedSizeIsBufferedSizeWithoutAReport)
}

func testUnplayedSizeIsBufferedSizeWithoutAReport(t *testing.T) {
	m := newMux(t)
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
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	reportHeard(m)

	// The delay's worth of what is sent is not heard yet.
	if got, want := p.UnplayedSize(), p.BufferedSize()+delay*bytesPerFrame; got != want {
		t.Errorf("at the report: UnplayedSize() = %d; want BufferedSize() and the delay, %d", got, want)
	}
	// It is heard as the clock moves on.
	time.Sleep(frameTime(400))
	if got, want := p.UnplayedSize(), p.BufferedSize()+(delay-400)*bytesPerFrame; got != want {
		t.Errorf("400 frames after the report: UnplayedSize() = %d; want %d", got, want)
	}
	// No more than is mixed is heard, however long the device goes without a
	// read.
	time.Sleep(frameTime(800))
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("1,200 frames after the report: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeCountsWhatIsSentBeforePause(t *testing.T) {
	synctest.Test(t, testUnplayedSizeCountsWhatIsSentBeforePause)
}

func testUnplayedSizeCountsWhatIsSentBeforePause(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	reportHeard(m)
	p.Pause()

	// What was sent before Pause is still to be heard, and is heard as the
	// device plays on.
	if got, want := p.UnplayedSize(), p.BufferedSize()+delay*bytesPerFrame; got != want {
		t.Errorf("right after Pause: UnplayedSize() = %d; want %d", got, want)
	}
	time.Sleep(frameTime(delay))
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("a delay after Pause: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeKeepsWhatIsSentWhileUnreported(t *testing.T) {
	synctest.Test(t, testUnplayedSizeKeepsWhatIsSentWhileUnreported)
}

func testUnplayedSizeKeepsWhatIsSentWhileUnreported(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	reportHeard(m)

	// As when the device is suspended, and resumed.
	m.ClearHeard()
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("after ClearHeard: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
	reportHeard(m)
	if got, want := p.UnplayedSize(), p.BufferedSize()+delay*bytesPerFrame; got != want {
		t.Errorf("reported again: UnplayedSize() = %d; want BufferedSize() and the delay, %d", got, want)
	}
}

func TestUnplayedSizeIgnoresAnOldReport(t *testing.T) {
	synctest.Test(t, testUnplayedSizeIgnoresAnOldReport)
}

func testUnplayedSizeIgnoresAnOldReport(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	// Frame 1,440, the next one mixed, is heard in 1.5 seconds.
	m.SetHeard(reportedFrame, time.Now().Add(1500*time.Millisecond))

	time.Sleep(time.Second)
	if got, want := p.UnplayedSize(), p.BufferedSize()+reportedFrame*bytesPerFrame; got != want {
		t.Errorf("a second after the report: UnplayedSize() = %d; want BufferedSize() and all that is sent, %d", got, want)
	}
	time.Sleep(time.Millisecond)
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("over a second after the report: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeIgnoresAFarReport(t *testing.T) {
	synctest.Test(t, testUnplayedSizeIgnoresAFarReport)
}

func testUnplayedSizeIgnoresAFarReport(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	reportHeard(m)

	// A report puts the frame over 2 seconds away.
	m.SetHeard(reportedFrame, time.Now().Add(2*time.Second+time.Millisecond))
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("after the far report: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}

func TestUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard(t *testing.T) {
	synctest.Test(t, testUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard)
}

func testUnplayedSizeCountsTheEndOfTheSourceUntilItIsHeard(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, reportedFrame)
	readFrames(m, reportedFrame)
	// The mux finds the source's end on its next read from it.
	synctest.Wait()
	if p.IsPlaying() {
		t.Fatal("the player plays on after its source is mixed")
	}
	reportHeard(m)
	if got, want := p.UnplayedSize(), delay*bytesPerFrame; got != want {
		t.Errorf("as the source ends: UnplayedSize() = %d; want %d", got, want)
	}
	time.Sleep(frameTime(delay))
	if got := p.UnplayedSize(); got != 0 {
		t.Errorf("a delay after the source ends: UnplayedSize() = %d; want 0", got)
	}
}

func TestUnplayedSizeDiscardsWhatIsSentBeforeSeek(t *testing.T) {
	synctest.Test(t, testUnplayedSizeDiscardsWhatIsSentBeforeSeek)
}

func testUnplayedSizeDiscardsWhatIsSentBeforeSeek(t *testing.T) {
	m := newMux(t)
	p := newBufferedPlayer(t, m, 4800)
	readFrames(m, reportedFrame)
	reportHeard(m)
	if _, err := p.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := p.UnplayedSize(), p.BufferedSize(); got != want {
		t.Errorf("after Seek: UnplayedSize() = %d; want BufferedSize(), %d", got, want)
	}
}
