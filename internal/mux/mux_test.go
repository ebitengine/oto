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
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ebitengine/oto/v3/internal/mux"
)

// blockingReader is a source that never returns from Read until unblock is closed.
type blockingReader struct {
	unblock chan struct{}
}

func (r *blockingReader) Read(buf []byte) (int, error) {
	<-r.unblock
	return 0, io.EOF
}

func (r *blockingReader) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}

func newBlockingPlayer(t *testing.T) *mux.Player {
	t.Helper()

	r := &blockingReader{
		unblock: make(chan struct{}),
	}
	t.Cleanup(func() {
		close(r.unblock)
	})
	return mux.New(48000, 2, mux.FormatSignedInt16LE).NewPlayer(r)
}

// Issue #270
func TestPlayDoesNotBlockOnSource(t *testing.T) {
	p := newBlockingPlayer(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Play()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Play blocked on reading the source")
	}
}

// Issue #270
func TestBufferIsFilledAfterPlay(t *testing.T) {
	const bufferSize = 4096

	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(bytes.NewReader(make([]byte, 2*bufferSize)))
	p.SetBufferSize(bufferSize)
	p.Play()

	deadline := time.Now().Add(time.Second)
	for p.BufferedSize() < bufferSize {
		if time.Now().After(deadline) {
			t.Fatalf("the buffer was not filled: got %d; want %d", p.BufferedSize(), bufferSize)
		}
		time.Sleep(time.Millisecond)
	}
}

// gatedReader is a source whose Read does not return until gate is closed.
// Read fails after Close is called.
type gatedReader struct {
	gate      chan struct{}
	began     chan struct{}
	beganOnce sync.Once
	reads     atomic.Int64

	m      sync.Mutex
	closed bool
}

func (r *gatedReader) Read(buf []byte) (int, error) {
	r.reads.Add(1)
	r.beganOnce.Do(func() {
		close(r.began)
	})
	<-r.gate

	r.m.Lock()
	defer r.m.Unlock()
	if r.closed {
		return 0, errors.New("gatedReader: read after close")
	}
	for i := range buf {
		buf[i] = 0
	}
	return len(buf), nil
}

func (r *gatedReader) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}

func (r *gatedReader) Close() error {
	r.m.Lock()
	defer r.m.Unlock()
	r.closed = true
	return nil
}

// rampReader is a source that produces an incrementing byte sequence.
type rampReader struct {
	m   sync.Mutex
	pos int
}

func (r *rampReader) Read(buf []byte) (int, error) {
	r.m.Lock()
	defer r.m.Unlock()
	for i := range buf {
		buf[i] = byte(r.pos + i)
	}
	r.pos += len(buf)
	return len(buf), nil
}

func (r *rampReader) position() int {
	r.m.Lock()
	defer r.m.Unlock()
	return r.pos
}

// Issue #288
func TestClosingSourceWhilePausedCausesError(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	// Pause does not stop the in-flight read.
	p.Pause()
	_ = src.Close()
	close(src.gate)

	// The in-flight read fails as the source is already closed, and the player gets the error.
	deadline := time.Now().Add(time.Second)
	for p.Err() == nil {
		if time.Now().After(deadline) {
			t.Error("Err did not return an error after the source was closed while paused")
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// Issue #290
func TestPauseAndStopReadingKeepsOngoingReadResult(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.PauseAndStopReading()
	}()

	select {
	case <-done:
		t.Fatal("PauseAndStopReading returned while a read from the source was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the ongoing read finish. PauseAndStopReading should return soon.
	close(src.gate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PauseAndStopReading did not return after the ongoing read finished")
	}

	if p.BufferedSize() == 0 {
		t.Error("the result of the ongoing read was discarded")
	}

	// After PauseAndStopReading, the source must not be read until Play is called.
	reads := src.reads.Load()
	time.Sleep(100 * time.Millisecond)
	if got := src.reads.Load(); got != reads {
		t.Errorf("the source was read after PauseAndStopReading: got %d reads; want %d", got, reads)
	}
	if err := p.Err(); err != nil {
		t.Errorf("Err after PauseAndStopReading: got %v; want nil", err)
	}

	// The player must be reusable after PauseAndStopReading.
	// Consume the buffered data to make a room for a new read.
	p.Play()
	m.ReadFloat32s(make([]float32, 4096))
	deadline := time.Now().Add(time.Second)
	for src.reads.Load() == reads {
		if time.Now().After(deadline) {
			t.Error("the source was not read after Play following PauseAndStopReading")
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// Issue #290
func TestPauseAndStopReadingDoesNotLoseData(t *testing.T) {
	const bufferSize = 1024

	src := &rampReader{}
	m := mux.New(48000, 1, mux.FormatUnsignedInt8)
	p := m.NewPlayer(src)
	p.SetBufferSize(bufferSize)
	p.Play()

	// readPlayedBytes reads the data that the player has already buffered,
	// and converts it back to the original byte values.
	readPlayedBytes := func(max int) []byte {
		n := min(p.BufferedSize(), max)
		if n == 0 {
			return nil
		}
		buf := make([]float32, n)
		m.ReadFloat32s(buf)
		bs := make([]byte, n)
		for i, v := range buf {
			bs[i] = byte(int(v*(1<<7)) + (1 << 7))
		}
		return bs
	}

	var played []byte
	deadline := time.Now().Add(time.Second)
	for len(played) < bufferSize {
		if time.Now().After(deadline) {
			t.Fatalf("the player did not play enough data: got %d bytes; want %d", len(played), bufferSize)
		}
		played = append(played, readPlayedBytes(64)...)
		time.Sleep(time.Millisecond)
	}

	p.PauseAndStopReading()

	buffered := p.BufferedSize()
	if buffered == 0 {
		t.Error("PauseAndStopReading dropped the buffered data")
	}

	// The source must not be read, and the buffer must be kept, while the player is stopped.
	pos := src.position()
	time.Sleep(100 * time.Millisecond)
	if got := src.position(); got != pos {
		t.Errorf("the source was read after PauseAndStopReading: got position %d; want %d", got, pos)
	}
	if got := p.BufferedSize(); got != buffered {
		t.Errorf("BufferedSize after PauseAndStopReading: got %d; want %d", got, buffered)
	}

	p.Play()

	deadline = time.Now().Add(time.Second)
	for len(played) < 4*bufferSize {
		if time.Now().After(deadline) {
			t.Fatalf("the player did not play enough data after resuming: got %d bytes; want %d", len(played), 4*bufferSize)
		}
		played = append(played, readPlayedBytes(64)...)
		time.Sleep(time.Millisecond)
	}

	// The played data must be continuous. No byte read from the source may be skipped.
	for i, b := range played {
		if b != byte(i) {
			t.Fatalf("played[%d]: got %d; want %d (%d bytes were lost around here)", i, b, byte(i), int(b)-i)
		}
	}
}

// Issue #290
func TestClosingSourceAfterPauseAndStopReadingIsSafe(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.PauseAndStopReading()
	}()

	// Let the ongoing read finish so that PauseAndStopReading can return.
	close(src.gate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PauseAndStopReading did not return after the ongoing read finished")
	}

	_ = src.Close()
	reads := src.reads.Load()

	// Consume the mux's buffer to tempt it to read the source again.
	m.ReadFloat32s(make([]float32, 256))
	time.Sleep(100 * time.Millisecond)

	if got := src.reads.Load(); got != reads {
		t.Errorf("the source was read after PauseAndStopReading: got %d reads; want %d", got, reads)
	}
	if err := p.Err(); err != nil {
		t.Errorf("Err after closing the source following PauseAndStopReading: got %v; want nil", err)
	}
}

func TestResetClearsBuffer(t *testing.T) {
	const bufferSize = 1024

	src := &rampReader{}
	m := mux.New(48000, 1, mux.FormatUnsignedInt8)
	p := m.NewPlayer(src)
	p.SetBufferSize(bufferSize)
	p.Play()

	deadline := time.Now().Add(time.Second)
	for p.BufferedSize() < bufferSize {
		if time.Now().After(deadline) {
			t.Fatalf("the buffer was not filled: got %d; want %d", p.BufferedSize(), bufferSize)
		}
		time.Sleep(time.Millisecond)
	}

	p.Reset()

	if got := p.BufferedSize(); got != 0 {
		t.Errorf("BufferedSize after Reset: got %d; want 0", got)
	}
}

// Issue #290
func TestSeekDiscardsOngoingReadResult(t *testing.T) {
	src := &gatedReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	// Pause not to resume playing after seeking.
	p.Pause()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Seek(0, io.SeekStart); err != nil {
			t.Error(err)
		}
	}()

	// Let the ongoing read finish so that Seek can return.
	close(src.gate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Seek did not return after the ongoing read finished")
	}

	// The ongoing read is data at the old position and must be discarded.
	time.Sleep(100 * time.Millisecond)
	if got := p.BufferedSize(); got != 0 {
		t.Errorf("BufferedSize after Seek: got %d; want 0", got)
	}
}

// seekingReader is a source that reports whether Seek was called while Read was in flight.
// Read does not return until gate is closed.
type seekingReader struct {
	gate      chan struct{}
	began     chan struct{}
	beganOnce sync.Once

	reading    atomic.Bool
	overlapped atomic.Bool
}

func (r *seekingReader) Read(buf []byte) (int, error) {
	r.reading.Store(true)
	defer r.reading.Store(false)

	r.beganOnce.Do(func() {
		close(r.began)
	})
	<-r.gate

	for i := range buf {
		buf[i] = 0
	}
	return len(buf), nil
}

func (r *seekingReader) Seek(offset int64, whence int) (int64, error) {
	if r.reading.Load() {
		r.overlapped.Store(true)
	}
	return 0, nil
}

// Issue #290
func TestSeekWaitsForOngoingRead(t *testing.T) {
	src := &seekingReader{
		gate:  make(chan struct{}),
		began: make(chan struct{}),
	}
	m := mux.New(48000, 2, mux.FormatSignedInt16LE)
	p := m.NewPlayer(src)
	p.Play()

	// Wait until a read from the source is in flight.
	<-src.began

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Seek(0, io.SeekStart); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-done:
		t.Fatal("Seek returned while a read from the source was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the ongoing read finish. Seek should return soon.
	close(src.gate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Seek did not return after the ongoing read finished")
	}

	// The source must not be sought and read at the same time.
	if src.overlapped.Load() {
		t.Error("the source was sought while a read from the source was in flight")
	}
}

// constReader is a source that produces a constant byte value.
type constReader struct {
	value byte
}

func (r *constReader) Read(buf []byte) (int, error) {
	for i := range buf {
		buf[i] = r.value
	}
	return len(buf), nil
}

// float32Reader is a source that produces a constant float32 value as FormatFloat32LE data.
type float32Reader struct {
	value float32

	m   sync.Mutex
	pos int
}

func (r *float32Reader) Read(buf []byte) (int, error) {
	r.m.Lock()
	defer r.m.Unlock()
	bits := math.Float32bits(r.value)
	for i := range buf {
		buf[i] = byte(bits >> (8 * ((r.pos + i) % 4)))
	}
	r.pos += len(buf)
	return len(buf), nil
}

// constSample is the float32 value that a player created by newConstPlayer produces at the volume 1.
const constSample = 0.5

// newConstPlayer creates a playing player whose source produces constSample, with the given volume.
func newConstPlayer(t *testing.T, m *mux.Mux, volume float64) *mux.Player {
	t.Helper()

	// (192 - (1 << 7)) / (1 << 7) is constSample.
	p := m.NewPlayer(&constReader{value: 192})
	// Set the volume before playing so that the volume is not ramped while mixing.
	p.SetVolume(volume)
	p.Play()
	return p
}

func waitForBufferedSize(t *testing.T, p *mux.Player, size int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for p.BufferedSize() < size {
		if time.Now().After(deadline) {
			t.Fatalf("the buffer was not filled: got %d; want %d", p.BufferedSize(), size)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInvalidVolumeDoesNotAffectOtherPlayers(t *testing.T) {
	const sampleCount = 256

	// 1e39 is larger than math.MaxFloat32 and becomes +Inf when narrowed to float32.
	for _, volume := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 1e39} {
		t.Run(fmt.Sprintf("volume=%v", volume), func(t *testing.T) {
			m := mux.New(48000, 1, mux.FormatUnsignedInt8)
			p0 := newConstPlayer(t, m, 1)
			p1 := newConstPlayer(t, m, volume)

			if got, want := p1.Volume(), 0.0; got != want {
				t.Errorf("Volume after SetVolume(%v): got %v; want %v", volume, got, want)
			}

			waitForBufferedSize(t, p0, sampleCount)
			waitForBufferedSize(t, p1, sampleCount)

			buf := make([]float32, sampleCount)
			m.ReadFloat32s(buf)

			// Only p0 contributes to the mixed samples.
			for i, got := range buf {
				if want := float32(constSample); got != want {
					t.Fatalf("buf[%d]: got %v; want %v", i, got, want)
				}
			}
		})
	}
}

func TestInvalidVolumeWhilePlayingIsRecoverable(t *testing.T) {
	const sampleCount = 256

	m := mux.New(48000, 1, mux.FormatUnsignedInt8)
	p := newConstPlayer(t, m, 1)

	p.SetVolume(math.NaN())

	// The volume is ramped from the volume before, and the samples must stay finite meanwhile.
	buf := make([]float32, sampleCount)
	for range 2 {
		waitForBufferedSize(t, p, sampleCount)
		m.ReadFloat32s(buf)
		for i, got := range buf {
			if math.IsNaN(float64(got)) {
				t.Fatalf("buf[%d] with a NaN volume: got NaN; want a finite value", i)
			}
		}
	}

	// The player must be audible again after a valid volume is set.
	p.SetVolume(1)
	var sum float32
	for range 2 {
		waitForBufferedSize(t, p, sampleCount)
		m.ReadFloat32s(buf)
		for i, got := range buf {
			if math.IsNaN(float64(got)) {
				t.Fatalf("buf[%d] after restoring the volume: got NaN; want a finite value", i)
			}
			sum += got
		}
	}
	if sum == 0 {
		t.Error("the player stayed silent after a valid volume was set again")
	}
}

func TestVolumeLargerThanOneAmplifies(t *testing.T) {
	const sampleCount = 256

	m := mux.New(48000, 1, mux.FormatUnsignedInt8)
	p := newConstPlayer(t, m, 2)
	waitForBufferedSize(t, p, sampleCount)

	buf := make([]float32, sampleCount)
	m.ReadFloat32s(buf)
	for i, got := range buf {
		if want := float32(2 * constSample); got != want {
			t.Fatalf("buf[%d]: got %v; want %v", i, got, want)
		}
	}
}

func TestNonFiniteSourceSampleDoesNotAffectOtherPlayers(t *testing.T) {
	const sampleCount = 256

	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprintf("sample=%v", value), func(t *testing.T) {
			m := mux.New(48000, 1, mux.FormatFloat32LE)
			p0 := m.NewPlayer(&float32Reader{value: constSample})
			p0.Play()
			p1 := m.NewPlayer(&float32Reader{value: float32(value)})
			p1.Play()

			byteLength := mux.FormatFloat32LE.ByteLength()
			waitForBufferedSize(t, p0, sampleCount*byteLength)
			waitForBufferedSize(t, p1, sampleCount*byteLength)

			buf := make([]float32, sampleCount)
			m.ReadFloat32s(buf)

			// The non-finite samples are skipped, and only p0 contributes to the mixed samples.
			for i, got := range buf {
				if want := float32(constSample); got != want {
					t.Fatalf("buf[%d]: got %v; want %v", i, got, want)
				}
			}
		})
	}
}

func TestMixedSamplesStayFinite(t *testing.T) {
	const sampleCount = 256

	// Each player alone is within the float32 range, but their sum is not.
	m := mux.New(48000, 1, mux.FormatFloat32LE)
	var players []*mux.Player
	for range 2 {
		p := m.NewPlayer(&float32Reader{value: 1})
		p.SetVolume(3e38)
		p.Play()
		players = append(players, p)
	}

	byteLength := mux.FormatFloat32LE.ByteLength()
	for _, p := range players {
		waitForBufferedSize(t, p, sampleCount*byteLength)
	}

	buf := make([]float32, sampleCount)
	m.ReadFloat32s(buf)

	for i, got := range buf {
		if math.IsNaN(float64(got)) || math.IsInf(float64(got), 0) {
			t.Fatalf("buf[%d]: got %v; want a finite value", i, got)
		}
		if got == 0 {
			t.Fatalf("buf[%d]: got 0; want a non-zero value", i)
		}
	}
}
