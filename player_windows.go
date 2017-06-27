// Copyright 2015 Hajime Hoshi
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

// +build !js

package oto

import (
	"errors"
	"runtime"
	"unsafe"
)

type header struct {
	buffer  []uint8
	waveHdr *wavehdr
}

func newHeader(waveOut uintptr, bufferSize int) (*header, error) {
	h := &header{
		buffer: make([]uint8, bufferSize),
	}
	h.waveHdr = &wavehdr{
		lpData:         uintptr(unsafe.Pointer(&h.buffer[0])),
		dwBufferLength: uint32(bufferSize),
	}
	if err := waveOutPrepareHeader(waveOut, h.waveHdr); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *header) Write(waveOut uintptr, data []byte) error {
	if len(data) != len(h.buffer) {
		return errors.New("oto: len(data) must equal to len(h.buffer)")
	}
	copy(h.buffer, data)
	if err := waveOutWrite(waveOut, h.waveHdr); err != nil {
		return err
	}
	return nil
}

type player struct {
	out       uintptr
	headers   []*header
	tmpBuffer []uint8
}

func newPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes int) (*player, error) {
	numBlockAlign := channelNum * bytesPerSample
	f := &waveformatex{
		wFormatTag:      waveFormatPCM,
		nChannels:       uint16(channelNum),
		nSamplesPerSec:  uint32(sampleRate),
		nAvgBytesPerSec: uint32(sampleRate * numBlockAlign),
		wBitsPerSample:  uint16(bytesPerSample * 8),
		nBlockAlign:     uint16(numBlockAlign),
	}
	w, err := waveOutOpen(f)
	if err != nil {
		return nil, err
	}
	p := &player{
		out:             w,
		headers:         make([]*header, bufferUnitNum(bufferSizeInBytes)),
	}
	runtime.SetFinalizer(p, (*player).Close)
	for i := range p.headers {
		var err error
		p.headers[i], err = newHeader(w, bufferUnitSize)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *player) Write(data []uint8) (int, error) {
	n := min(len(data), bufferUnitSize-len(p.tmpBuffer))
	p.tmpBuffer = append(p.tmpBuffer, data[:n]...)
	if len(p.tmpBuffer) < bufferUnitSize {
		return n, nil
	}
	var headerToWrite *header
	for _, h := range p.headers {
		// TODO: Need to check WHDR_DONE?
		if h.waveHdr.dwFlags&whdrInqueue == 0 {
			headerToWrite = h
			break
		}
	}
	if headerToWrite == nil {
		// This can happen (hajimehoshi/ebiten#207)
		return n, nil
	}
	if err := headerToWrite.Write(p.out, p.tmpBuffer); err != nil {
		return 0, err
	}
	p.tmpBuffer = nil
	return n, nil
}

func (p *player) Close() error {
	runtime.SetFinalizer(p, nil)
	// TODO: Call waveOutUnprepareHeader here
	if err := waveOutClose(p.out); err != nil {
		return err
	}
	return nil
}
