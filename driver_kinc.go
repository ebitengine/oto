// Copyright 2022 Sam Hocevar <sam@hocevar.net>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the license.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build kinc
// +build kinc

package oto

/*
#include <kinc/audio2/audio.h>

typedef void (*audio_callback_t)(kinc_a2_buffer_t *buffer, int samples);
typedef void (*sample_rate_callback_t)(void);

void audio_callback(kinc_a2_buffer_t *buffer, int samples);
void sample_rate_callback(void);
*/
import "C"

import (
	"sync"
	"unsafe"
)

const (
	MaxBufferLength = 4096 // Max number of samples we want in the audio queue
)

type driver struct {
	buf	[]int16
	mutex	sync.Mutex
}

var gDriver *driver

func (buffer *C.kinc_a2_buffer_t) SampleCount() int {
	return int((buffer.write_location + buffer.data_size - buffer.read_location) % buffer.data_size) / 4
}

//export audio_callback
func audio_callback(buffer *C.kinc_a2_buffer_t, samples int32) {
	gDriver.mutex.Lock()
	defer gDriver.mutex.Unlock()

	// Protect against accessing gDriver.buf[0]
	if len(gDriver.buf) == 0 {
		return
	}

	// Bluntly drop data if our buffer is too full
	if buffer.SampleCount() >= MaxBufferLength {
		gDriver.buf = gDriver.buf[:]
		return
	}

	dst := unsafe.Slice((*float32)(unsafe.Pointer(buffer.data)), int(buffer.data_size) / 4)
	for i := 0; i < int(samples); i++ {
		if i < len(gDriver.buf) {
			dst[buffer.write_location / 4] = float32(gDriver.buf[i]) / 32768
		} else {
			dst[buffer.write_location / 4] = 0
		}

		buffer.write_location += 4
		if buffer.write_location >= buffer.data_size {
			buffer.write_location = 0
		}
	}
	gDriver.buf = gDriver.buf[min(len(gDriver.buf), int(samples)):]
}

//export sample_rate_callback
func sample_rate_callback() {
	// TODO: handle rate changes?
}

func newDriver(sampleRate, numChans, bitDepthInBytes, bufferSizeInBytes int) (tryWriteCloser, error) {
	p := &driver{
		buf: []int16{},
	}

	C.kinc_a2_init()
	C.kinc_a2_set_callback((C.audio_callback_t)(C.audio_callback))
	gDriver = p

	return p, nil
}

func (p *driver) TryWrite(data []byte) (n int, err error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	samples := unsafe.Slice((*int16)(unsafe.Pointer(&data[0])), len(data) / 2)
	sampleCount := min(len(samples), max(0, MaxBufferLength - len(p.buf)))
	p.buf = append(p.buf, samples[:sampleCount]...)
	return sampleCount * 2, nil
}

func (p *driver) Close() error {
	C.kinc_a2_shutdown()
	gDriver = nil

	return nil
}

func (d *driver) tryWriteCanReturnWithoutWaiting() bool {
	return true
}
