// Copyright 2017 Hajime Hoshi
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

// Package oto offers io.Writer to play sound on multiple platforms.
package oto

import (
	"time"
)

// A Player is a sound player.
type Player struct {
	player         *player
	sampleRate     int
	channelNum     int
	bytesPerSample int
	bufferSize     int
}

// NewPlayer creates a Player.
//
// sampleRate indicates the sample rate like 2048.
//
// channelNum indicates the number of channels. This must be 1 or 2.
//
// bytesPerSample indicates the size of a sample in one channel. This must be 1 or 2.
//
// bufferSizeInBytes indicates the size in bytes of inner buffers.
// Too small buffer can trigger glitches, and too big buffer can trigger delay.
//
// NewPlayer returns error when initializaiton fails.
func NewPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes int) (*Player, error) {
	p, err := newPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes)
	if err != nil {
		return nil, err
	}
	return &Player{
		player:         p,
		sampleRate:     sampleRate,
		channelNum:     channelNum,
		bytesPerSample: bytesPerSample,
		bufferSize:     bufferSizeInBytes,
	}, nil
}

func (p *Player) bytesPerSec() int {
	return p.sampleRate * p.channelNum * p.bytesPerSample
}

// Write is io.Writer's Write.
//
// The format is 8bit or 16bit (little endian), 1 or 2 channel PCM.
//
// For example, if the number of channels is 2 and the size of a sample is 2, the format is:
//
//    [Left lower byte][Left higher byte][Right lower byte][Right higher byte]...(repeat)...
func (p *Player) Write(data []uint8) (int, error) {
	written := 0
	total := len(data)
	// TODO: Fix player's Write to satisfy io.Writer.
	// Now player's Write doesn't satisfy io.Writer's requirements since
	// the current Write might return without processing all given data.
	for written < total {
		n, err := p.player.Write(data)
		written += n
		if err != nil {
			return written, err
		}
		data = data[n:]
		// When not all data is written, the underlying buffer is full.
		// Mitigate the busy loop by sleeping (#10).
		if n == 0 {
			t := time.Second * time.Duration(p.bufferSize) / time.Duration(p.bytesPerSec()) / 4
			time.Sleep(t)
		}
	}
	return written, nil
}

// Close is io.Closer's Close.
func (p *Player) Close() error {
	return p.player.Close()
}

func max(a, b int) int {
	if a < b {
		return b
	}
	return a
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const bufferUnitSize = 512

func bufferUnitNum(bufferSize int) int {
	u := max(bufferSize, bufferUnitSize)
	return max(u/bufferUnitSize, 2)
}
