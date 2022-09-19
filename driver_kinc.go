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
	"unsafe"
)

const (
	BufferLength = 4096 // Max number of samples in our audio buffer
)

type driver struct {
	buf	[]byte // Holds int16 data
}

var gDriver *driver

//export audio_callback
func audio_callback(buffer *C.kinc_a2_buffer_t, samples int32) {
	// TODO: buffer.write_location and buffer.read_location may give us a good
	// hint about the buffer status and decide whether we need to hurry up to
	// avoid stuttering.
	canRead := len(gDriver.buf) / 2
	toWrite := min(canRead, int(samples))
	for i := 0; i < toWrite; i++ {

		sample := *(*int16)(unsafe.Pointer(uintptr(unsafe.Pointer(&gDriver.buf[0])) + uintptr(i * 2)))
		*(*float32)(unsafe.Pointer(uintptr(unsafe.Pointer(buffer.data)) + uintptr(buffer.write_location))) = float32(sample) / 32768.

		buffer.write_location += 4
		if buffer.write_location >= buffer.data_size {
			buffer.write_location = 0
		}
	}
	gDriver.buf = gDriver.buf[toWrite * 2:]
}

//export sample_rate_callback
func sample_rate_callback() {
	// TODO: handle rate changes?
}

func newDriver(sampleRate, numChans, bitDepthInBytes, bufferSizeInBytes int) (tryWriteCloser, error) {
	p := &driver{
		[]byte{},
	}

	C.kinc_a2_init()
	C.kinc_a2_set_callback((C.audio_callback_t)(C.audio_callback))
	gDriver = p

	return p, nil
}

func (p *driver) TryWrite(data []byte) (n int, err error) {
	toAppend := min(len(data), max(0, BufferLength - len(p.buf)))
	p.buf = append(p.buf, data[:toAppend]...)
	return toAppend, nil
}

func (p *driver) Close() error {
	C.kinc_a2_shutdown()
	gDriver = nil

	return nil
}

func (d *driver) tryWriteCanReturnWithoutWaiting() bool {
	return true
}
