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
	"errors"
	"reflect"
	"runtime"
	"syscall/js"
	"unsafe"
)

type context struct {
	sampleRate      int
	channelCount    int
	bitDepthInBytes int

	audioContext            js.Value
	scriptProcessor         js.Value
	scriptProcessorCallback js.Func
	ready                   bool
	callbacks               map[string]js.Func

	players *players
}

func newContext(sampleRate int, channelCount int, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})

	class := js.Global().Get("AudioContext")
	if !class.Truthy() {
		class = js.Global().Get("webkitAudioContext")
	}
	if !class.Truthy() {
		return nil, nil, errors.New("oto: AudioContext or webkitAudioContext was not found")
	}
	options := js.Global().Get("Object").New()
	options.Set("sampleRate", sampleRate)

	d := &context{
		sampleRate:      sampleRate,
		channelCount:    channelCount,
		bitDepthInBytes: bitDepthInBytes,
		audioContext:    class.New(options),
		players:         newPlayers(),
	}

	// 4096 was not great at least on Safari 15.
	bufferSizeInBytes := 8192 * channelCount

	buf32 := make([]float32, bufferSizeInBytes/4)
	chBuf32 := make([][]float32, channelCount)
	for i := range chBuf32 {
		chBuf32[i] = make([]float32, len(buf32)/channelCount)
	}

	// TODO: Use AudioWorklet if available.
	sp := d.audioContext.Call("createScriptProcessor", bufferSizeInBytes/4/channelCount, 0, channelCount)
	f := js.FuncOf(func(this js.Value, arguments []js.Value) interface{} {
		d.players.read(buf32)
		for i := 0; i < channelCount; i++ {
			for j := range chBuf32[i] {
				chBuf32[i][j] = buf32[j*channelCount+i]
			}
		}

		buf := arguments[0].Get("outputBuffer")
		if buf.Get("copyToChannel").Truthy() {
			for i := 0; i < channelCount; i++ {
				buf.Call("copyToChannel", float32SliceToTypedArray(chBuf32[i]), i, 0)
			}
		} else {
			// copyToChannel is not defined on Safari 11.
			for i := 0; i < channelCount; i++ {
				buf.Call("getChannelData", i).Call("set", float32SliceToTypedArray(chBuf32[i]))
			}
		}
		return nil
	})
	sp.Call("addEventListener", "audioprocess", f)
	d.scriptProcessor = sp
	d.scriptProcessorCallback = f

	sp.Call("connect", d.audioContext.Get("destination"))

	setCallback := func(event string) js.Func {
		var f js.Func
		f = js.FuncOf(func(this js.Value, arguments []js.Value) interface{} {
			if !d.ready {
				d.audioContext.Call("resume")
				d.ready = true
				close(ready)
			}
			js.Global().Get("document").Call("removeEventListener", event, f)
			return nil
		})
		js.Global().Get("document").Call("addEventListener", event, f)
		d.callbacks[event] = f
		return f
	}

	// Browsers require user interaction to start the audio.
	// https://developers.google.com/web/updates/2017/09/autoplay-policy-changes#webaudio
	d.callbacks = map[string]js.Func{}
	setCallback("touchend")
	setCallback("keyup")
	setCallback("mouseup")

	return d, ready, nil
}

func (c *context) Suspend() error {
	c.audioContext.Call("suspend")
	return nil
}

func (c *context) Resume() error {
	c.audioContext.Call("resume")
	return nil
}

func (c *context) Err() error {
	return nil
}

func float32SliceToTypedArray(s []float32) js.Value {
	h := (*reflect.SliceHeader)(unsafe.Pointer(&s))
	h.Len *= 4
	h.Cap *= 4
	bs := *(*[]byte)(unsafe.Pointer(h))

	a := js.Global().Get("Uint8Array").New(len(bs))
	js.CopyBytesToJS(a, bs)
	runtime.KeepAlive(s)
	buf := a.Get("buffer")
	return js.Global().Get("Float32Array").New(buf, a.Get("byteOffset"), a.Get("byteLength").Int()/4)
}
