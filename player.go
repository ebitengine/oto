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

package oto

type Player struct {
	player *player
}

func NewPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes int) (*Player, error) {
	p, err := newPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes)
	if err != nil {
		return nil, err
	}
	return &Player{p}, nil
}

func (p *Player) Write(data []uint8) (int, error) {
	return p.player.Write(data)
}

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
