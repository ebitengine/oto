// Copyright 2021 The Oto Authors
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
	"io"
	"sync"
)

// Context is the main object in Oto. It interacts with the audio drivers.
//
// To play sound with Oto, first create a context. Then use the context to create
// an arbitrary number of players. Then use the players to play sound.
//
// Creating multiple contexts is NOT supported.
type Context struct {
	context *context
}

// NewPlayer creates a new, ready-to-use Player belonging to the Context.
// It is safe to create multiple players.
//
// The format of r is as follows:
//   [data]      = [sample 1] [sample 2] [sample 3] ...
//   [sample *]  = [channel 1] [channel 2] ...
//   [channel *] = [byte 1] [byte 2] ...
// Byte ordering is little endian.
//
// A player has some amount of an underlying buffer.
// Read data from r is queued to the player's underlying buffer.
// The underlying buffer is consumed by its playing.
// Then, r's position and the current playing position don't necessarily match.
// If you want to clear the underlying buffer for some reasons e.g., you want to seek the position of r,
// call the player's Reset function.
//
// You cannot share r by multiple players.
//
// The returned player implements Player, BufferSizeSetter, and io.Seeker.
// You can modify the buffer size of a player by the SetBufferSize function.
// A small buffer size is useful if you want to play a real-time PCM for example.
// Note that the audio quality might be affected if you modify the buffer size.
//
// If r does not implement io.Seeker, the returned player's Seek returns an error.
//
// NewPlayer is concurrent-safe.
//
// All the functions of a Player returned by NewPlayer are concurrent-safe.
func (c *Context) NewPlayer(r io.Reader) Player {
	return c.context.NewPlayer(r)
}

// Suspend suspends the entire audio play.
//
// Suspend is concurrent-safe.
func (c *Context) Suspend() error {
	return c.context.Suspend()
}

// Resume resumes the entire audio play, which was suspended by Suspend.
//
// Resume is concurrent-safe.
func (c *Context) Resume() error {
	return c.context.Resume()
}

// Err returns the current error.
//
// Err is concurrent-safe.
func (c *Context) Err() error {
	return c.context.Err()
}

// NewContext creates a new context, that creates and holds ready-to-use Player objects,
// and returns a context, a channel that is closed when the context is ready, and an error if it exists.
//
// Creating multiple contexts is NOT supported.
//
// The sampleRate argument specifies the number of samples that should be played during one second.
// Usual numbers are 44100 or 48000. One context has only one sample rate. You cannot play multiple audio
// sources with different sample rates at the same time.
//
// The channelCount argument specifies the number of channels. One channel is mono playback. Two
// channels are stereo playback. No other values are supported.
//
// The bitDepthInBytes argument specifies the number of bytes per sample per channel. The usual value
// is 2. Only values 1 and 2 are supported.
func NewContext(sampleRate int, channelCount int, bitDepthInBytes int) (*Context, chan struct{}, error) {
	ctx, ready, err := newContext(sampleRate, channelCount, bitDepthInBytes)
	if err != nil {
		return nil, nil, err
	}
	return &Context{context: ctx}, ready, nil
}

// Player is a PCM (pulse-code modulation) audio player.
type Player interface {
	// Pause pauses its playing.
	Pause()

	// Play starts its playing if it doesn't play.
	Play()

	// IsPlaying reports whether this player is playing.
	IsPlaying() bool

	// Reset clears the underyling buffer and pauses its playing.
	// Deprecated: use Pause or Seek instead.
	Reset()

	// Volume returns the current volume in the range of [0, 1].
	// The default volume is 1.
	Volume() float64

	// SetVolume sets the current volume in the range of [0, 1].
	SetVolume(volume float64)

	// UnplayedBufferSize returns the byte size in the underlying buffer that is not played yet.
	UnplayedBufferSize() int

	// Err returns an error if this player has an error.
	Err() error

	io.Closer

	// A player returned at NewPlayer also implements BufferSizeSetter and io.Seeker, but
	// these are not defined in this interface for backward compatibility in v2.
}

// BufferSizeSetter sets a buffer size.
// A player created by (*Context).NewPlayer implments both Player and BufferSizeSetter.
type BufferSizeSetter interface {
	// SetBufferSize sets the buffer size.
	// If 0 is specified, the default buffer size is used.
	SetBufferSize(bufferSize int)
}

type playerState int

const (
	playerPaused playerState = iota
	playerPlay
	playerClosed
)

// TODO: The term 'buffer' is confusing. Name each buffer with good terms.

// defaultBufferSize returns the default size of the buffer for the audio source.
// This buffer is used when unreading on pausing the player.
func (c *context) defaultBufferSize() int {
	bytesPerSample := c.channelCount * c.bitDepthInBytes
	s := c.sampleRate * bytesPerSample / 2 // 0.5[s]
	// Align s in multiples of bytes per sample, or a buffer could have extra bytes.
	return s / bytesPerSample * bytesPerSample
}

type atomicError struct {
	err error
	m   sync.Mutex
}

func (a *atomicError) TryStore(err error) {
	a.m.Lock()
	defer a.m.Unlock()
	if a.err == nil {
		a.err = err
	}
}

func (a *atomicError) Load() error {
	a.m.Lock()
	defer a.m.Unlock()
	return a.err
}
