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
)

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
