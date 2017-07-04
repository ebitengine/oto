// Copyright 2016 Hajime Hoshi
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

// TODO: Can we unify this into player_openal.go?

// +build ios

package oto

import (
	"fmt"
	"runtime"

	"golang.org/x/mobile/exp/audio/al"
)

type player struct {
	alSource   al.Source
	sampleRate int
	isClosed   bool
	alFormat   uint32

	bufs       []al.Buffer
	tmp        []uint8
	bufferSize int
}

func alFormat(channelNum, bytesPerSample int) uint32 {
	switch {
	case channelNum == 1 && bytesPerSample == 1:
		return al.FormatMono8
	case channelNum == 1 && bytesPerSample == 2:
		return al.FormatMono16
	case channelNum == 2 && bytesPerSample == 1:
		return al.FormatStereo8
	case channelNum == 2 && bytesPerSample == 2:
		return al.FormatStereo16
	}
	panic(fmt.Sprintf("oto: invalid channel num (%d) or bytes per sample (%d)", channelNum, bytesPerSample))
}

func newPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes int) (*player, error) {
	var p *player
	if err := al.OpenDevice(); err != nil {
		return nil, fmt.Errorf("oto: OpenAL initialization failed: %v", err)
	}

	s := al.GenSources(1)
	if e := al.Error(); e != 0 {
		return nil, fmt.Errorf("oto: al.GenSources error: %d", e)
	}

	const numBufs = 2
	p = &player{
		alSource:   s[0],
		sampleRate: sampleRate,
		alFormat:   alFormat(channelNum, bytesPerSample),
		bufs:       al.GenBuffers(numBufs),
		bufferSize: bufferSizeInBytes,
	}
	runtime.SetFinalizer(p, (*player).Close)
	al.PlaySources(p.alSource)

	return p, nil
}

func (p *player) Write(data []byte) (int, error) {
	if err := al.Error(); err != 0 {
		return 0, fmt.Errorf("oto: before Write: %d", err)
	}
	n := min(len(data), p.bufferSize-len(p.tmp))
	p.tmp = append(p.tmp, data[:n]...)
	if len(p.tmp) < p.bufferSize {
		return n, nil
	}

	if pn := p.alSource.BuffersProcessed(); pn > 0 {
		bufs := make([]al.Buffer, pn)
		p.alSource.UnqueueBuffers(bufs...)
		if err := al.Error(); err != 0 {
			return 0, fmt.Errorf("oto: Unqueue: %d", err)
		}
		p.bufs = append(p.bufs, bufs...)
	}

	if len(p.bufs) == 0 {
		return n, nil
	}

	buf := p.bufs[0]
	p.bufs = p.bufs[1:]
	buf.BufferData(p.alFormat, p.tmp, int32(p.sampleRate))
	p.alSource.QueueBuffers(buf)
	if err := al.Error(); err != 0 {
		return 0, fmt.Errorf("oto: Queue: %d", err)
	}

	if p.alSource.State() == al.Stopped || p.alSource.State() == al.Initial {
		al.RewindSources(p.alSource)
		al.PlaySources(p.alSource)
		if err := al.Error(); err != 0 {
			return 0, fmt.Errorf("oto: PlaySource: %d", err)
		}
	}

	p.tmp = nil
	return n, nil
}

func (p *player) Close() error {
	if err := al.Error(); err != 0 {
		return fmt.Errorf("oto: error before closing: %d", err)
	}
	if p.isClosed {
		return nil
	}

	var bs []al.Buffer
	al.RewindSources(p.alSource)
	al.StopSources(p.alSource)
	if n := p.alSource.BuffersQueued(); 0 < n {
		bs = make([]al.Buffer, n)
		p.alSource.UnqueueBuffers(bs...)
		p.bufs = append(p.bufs, bs...)
	}

	p.isClosed = true
	if err := al.Error(); err != 0 {
		return fmt.Errorf("oto: error after closing: %d", err)
	}
	runtime.SetFinalizer(p, nil)

	return nil
}
