// Copyright 2022 The Oto Authors
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

//go:build nintendosdk
// +build nintendosdk

package oto

// #cgo !darwin LDFLAGS: -Wl,-unresolved-symbols=ignore-all
// #cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup
//
// typedef void (*OnReadCallback)(float* buf, size_t length);
//
// // TODO: This odd function name is for backward compatibility. Rename them.
// void EbitenOpenAudio(int sample_rate, int channel_num, OnReadCallback on_read_callback);
//
// void oto_OnReadCallback(float* buf, size_t length);
// static void oto_OpenProxy(int sample_rate, int channel_num) {
//   EbitenOpenAudio(sample_rate, channel_num, oto_OnReadCallback);
// }
import "C"

import (
	"reflect"
	"unsafe"

	"github.com/hajimehoshi/oto/v2/internal/mux"
)

//export oto_OnReadCallback
func oto_OnReadCallback(buf *C.float, length C.size_t) {
	var s []float32
	h := (*reflect.SliceHeader)(unsafe.Pointer(&s))
	h.Data = uintptr(unsafe.Pointer(buf))
	h.Len = int(length)
	h.Cap = int(length)
	theContext.mux.ReadFloat32s(s)
}

type context struct {
	mux *mux.Mux
}

var theContext *context

func newContext(sampleRate, channelCount, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		mux: mux.New(sampleRate, channelCount, bitDepthInBytes),
	}
	theContext = c
	C.oto_OpenProxy(C.int(sampleRate), C.int(channelCount))

	return c, ready, nil
}

func (c *context) Suspend() error {
	// Do nothing so far.
	return nil
}

func (c *context) Resume() error {
	// Do nothing so far.
	return nil
}

func (c *context) Err() error {
	return nil
}
