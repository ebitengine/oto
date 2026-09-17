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

package mux

func (p *Player) IsRegistered() bool {
	p.p.mux.playersMu.Lock()
	defer p.p.mux.playersMu.Unlock()

	_, ok := p.p.mux.players[p.p]
	return ok
}

// PrimeForMixing puts the player into the playing state with buf as its buffered
// source data and a volume ramp from prevVolume to the current volume, without
// registering the player with the mux. It is used to test the mixing directly.
func (p *Player) PrimeForMixing(buf []byte, prevVolume float64) {
	p.p.m.Lock()
	defer p.p.m.Unlock()

	p.p.state = playerPlay
	p.p.buf = buf
	p.p.prevVolume = prevVolume
}

// ReadBufferAndAdd mixes the buffered source data into buf and returns the number
// of mixed samples.
func (p *Player) ReadBufferAndAdd(buf []float32) int {
	return p.p.readBufferAndAdd(buf)
}
