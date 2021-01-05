// Copyright 2020 Hajime Hoshi
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
	"fmt"
	"sync"
	"syscall/js"
)

type go2CppDriver struct {
	writer     js.Value
	buf        js.Value
	cond       *sync.Cond
	bufferSize int
	written    int
}

func newDriverGo2Cpp(sampleRate, channelNum, bitDepthInBytes, bufferSize int) (tryWriteCloser, error) {
	writer := js.Global().Get("go2cpp").Call("createAudio", sampleRate, channelNum, bitDepthInBytes, bufferSize)
	d := &go2CppDriver{
		writer:     writer,
		buf:        js.Global().Get("Uint8Array").New(16),
		cond:       sync.NewCond(&sync.Mutex{}),
		bufferSize: bufferSize,
	}

	d.writer.Set("onBufferConsumed", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		d.cond.L.Lock()
		defer d.cond.L.Unlock()

		wasFull := d.written >= d.bufferSize
		n := args[0].Int()
		d.written -= n
		if d.written < 0 {
			panic("oto: buffer is consumed too quickly")
		}

		if wasFull && n > 0 {
			d.cond.Signal()
		}
		return nil
	}))

	return d, nil
}

func (d *go2CppDriver) TryWrite(data []byte) (int, error) {
	l := d.buf.Get("byteLength").Int()
	if l < len(data) {
		for l < len(data) {
			l *= 2
		}
		d.buf = js.Global().Get("Uint8Array").New(l)
	}
	js.CopyBytesToJS(d.buf, data)

	d.cond.L.Lock()
	defer d.cond.L.Unlock()

	for d.written >= d.bufferSize {
		d.cond.Wait()
	}

	result := d.writer.Call("sendDataToBuffer", d.buf, len(data))
	if result.Type() != js.TypeNumber {
		return 0, fmt.Errorf("oto: write failed: %s", result.String())
	}
	n := result.Int()
	d.written += n
	return n, nil
}

func (d *go2CppDriver) Close() error {
	d.writer.Call("close")
	return nil
}

func (d *go2CppDriver) tryWriteCanReturnWithoutWaiting() bool {
	return false
}
