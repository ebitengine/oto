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

// +build js

package oto

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"syscall/js"
)

type driver struct {
	sampleRate      int
	channelNum      int
	bitDepthInBytes int
	nextPos         float64
	tmp             []byte
	sent            int
	bufferSize      int
	context         js.Value
	workletNode     js.Value
	ready           bool
	callbacks       map[string]js.Func
	cond            *sync.Cond
}

const audioBufferSamples = 3200

func newDriver(sampleRate, channelNum, bitDepthInBytes, bufferSize int) (tryWriteCloser, error) {
	class := js.Global().Get("AudioContext")
	if class == js.Undefined() {
		class = js.Global().Get("webkitAudioContext")
	}
	if class == js.Undefined() {
		return nil, errors.New("oto: audio couldn't be initialized")
	}

	options := js.Global().Get("Object").New()
	options.Set("sampleRate", sampleRate)
	context := class.New(options)

	var node js.Value
	if js.Global().Get("AudioWorkletNode") != js.Undefined() && isAudioWorkletAvailable() {
		script := `
class EbitenAudioWorkletProcessor extends AudioWorkletProcessor {
  constructor() {
    super();

    this.buffers_ = [[], []];
    this.offsets_ = [0, 0];
    this.offsetsInArray_ = [0, 0];

    this.port.onmessage = (e) => {
      const bufs = e.data;
      for (let ch = 0; ch < bufs.length; ch++) {
        if (bufs[ch].length === 0) {
          return;
        }
        this.buffers_[ch].push(bufs[ch]);
      };
    };
  }

  bufferTotalLength(ch) {
    const sum = this.buffers_[ch].reduce((total, buf) => total + buf.length, 0);
    return sum - this.offsetsInArray_[ch];
  }

  consume(ch, i) {
    while (this.buffers_[ch][0].length <= i - this.offsets_[ch]) {
      this.offsets_[ch] += this.buffers_[ch][0].length;
      this.offsetsInArray_[ch] = 0;
      this.buffers_[ch].shift();
    }
    this.offsetsInArray_[ch]++;
    return this.buffers_[ch][0][i - this.offsets_[ch]];
  }

  process(inputs, outputs, parameters) {
    const out = outputs[0];

    if (this.bufferTotalLength(0) < out[0].length) {
      for (let ch = 0; ch < out.length; ch++) {
        for (let i = 0; i < out[ch].length; i++) {
          out[ch][i] = 0;
        }
      }
      return true;
    }

    for (let ch = 0; ch < out.length; ch++) {
      const offset = this.offsets_[ch] + this.offsetsInArray_[ch];
      for (let i = 0; i < out[ch].length; i++) {
        out[ch][i] = this.consume(ch, i + offset);
      }
    }

    this.port.postMessage(out[0].length)
    return true;
  }
}

registerProcessor('ebiten-audio-worklet-processor', EbitenAudioWorkletProcessor);`
		scriptURL := "data:application/javascript;base64," + base64.StdEncoding.EncodeToString([]byte(script))

		ch := make(chan error)
		context.Get("audioWorklet").Call("addModule", scriptURL).Call("then", js.FuncOf(func(js.Value, []js.Value) interface{} {
			close(ch)
			return nil
		})).Call("catch", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			err := args[0]
			ch <- fmt.Errorf("oto: error at addModule: %s: %s", err.Get("name").String(), err.Get("message").String())
			close(ch)
			return nil
		}))
		if err := <-ch; err != nil {
			return nil, err
		}

		options := js.Global().Get("Object").New()
		arr := js.Global().Get("Array").New()
		arr.Call("push", channelNum)
		options.Set("outputChannelCount", arr)
		node = js.Global().Get("AudioWorkletNode").New(context, "ebiten-audio-worklet-processor", options)
		node.Call("connect", context.Get("destination"))
	}

	bs := bufferSize
	if node == js.Undefined() {
		bs = max(bufferSize, audioBufferSamples*channelNum*bitDepthInBytes)
	} else {
		bs = max(bufferSize, 4096)
	}

	p := &driver{
		sampleRate:      sampleRate,
		channelNum:      channelNum,
		bitDepthInBytes: bitDepthInBytes,
		context:         context,
		workletNode:     node,
		bufferSize:      bs,
		cond:            sync.NewCond(&sync.Mutex{}),
	}

	if node != js.Undefined() {
		node.Get("port").Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			p.cond.L.Lock()
			defer p.cond.L.Unlock()

			n := args[0].Get("data").Int() * p.bitDepthInBytes * p.channelNum
			if n == 0 {
				return nil
			}

			notify := len(p.tmp) == p.bufferSize && len(p.tmp) == p.sent
			p.tmp = p.tmp[n:]
			p.sent -= n
			if notify {
				p.cond.Signal()
			}

			return nil
		}))
	}

	setCallback := func(event string) js.Func {
		var f js.Func
		f = js.FuncOf(func(this js.Value, arguments []js.Value) interface{} {
			if !p.ready {
				p.context.Call("resume")
				p.ready = true
			}
			js.Global().Get("document").Call("removeEventListener", event, f)
			return nil
		})
		js.Global().Get("document").Call("addEventListener", event, f)
		p.callbacks[event] = f
		return f
	}

	// Browsers require user interaction to start the audio.
	// https://developers.google.com/web/updates/2017/09/autoplay-policy-changes#webaudio
	p.callbacks = map[string]js.Func{}
	setCallback("touchend")
	setCallback("keyup")
	setCallback("mouseup")
	return p, nil
}

func toLR(data []byte) ([]float32, []float32) {
	const max = 1 << 15

	l := make([]float32, len(data)/4)
	r := make([]float32, len(data)/4)
	for i := 0; i < len(data)/4; i++ {
		l[i] = float32(int16(data[4*i])|int16(data[4*i+1])<<8) / max
		r[i] = float32(int16(data[4*i+2])|int16(data[4*i+3])<<8) / max
	}
	return l, r
}

func (p *driver) TryWrite(data []byte) (int, error) {
	if !p.ready {
		return 0, nil
	}

	if p.workletNode != js.Undefined() {
		p.cond.L.Lock()
		defer p.cond.L.Unlock()

		n := min(len(data), max(0, p.bufferSize-len(p.tmp)))
		p.tmp = append(p.tmp, data[:n]...)

		if len(p.tmp) < p.bufferSize {
			return n, nil
		}

		for len(p.tmp) == p.bufferSize && len(p.tmp) == p.sent {
			p.cond.Wait()
		}

		if len(p.tmp) == p.sent {
			return n, nil
		}

		l, r := toLR(p.tmp[p.sent:])
		// As Audio Worklet is available only on Go 1.13 and newer, freeing functions don't have to be
		// called. See isAudioWorkletAvailable.
		tl, _ := float32SliceToTypedArray(l)
		tr, _ := float32SliceToTypedArray(r)

		bufs := js.Global().Get("Array").New()
		bufs.Call("push", tl, tr)
		transfers := js.Global().Get("Array").New()
		transfers.Call("push", tl.Get("buffer"), tr.Get("buffer"))

		p.workletNode.Get("port").Call("postMessage", bufs, transfers)

		p.sent = len(p.tmp)

		return n, nil
	}

	n := min(len(data), max(0, p.bufferSize-len(p.tmp)))
	p.tmp = append(p.tmp, data[:n]...)

	c := p.context.Get("currentTime").Float()

	if p.nextPos < c {
		p.nextPos = c
	}

	// It's too early to enqueue a buffer.
	// Highly likely, there are two playing buffers now.
	if c+float64(p.bufferSize/p.bitDepthInBytes/p.channelNum)/float64(p.sampleRate) < p.nextPos {
		return n, nil
	}

	le := audioBufferSamples * p.bitDepthInBytes * p.channelNum
	if len(p.tmp) < le {
		return n, nil
	}

	buf := p.context.Call("createBuffer", p.channelNum, audioBufferSamples, p.sampleRate)
	l, r := toLR(p.tmp[:le])
	tl, freel := float32SliceToTypedArray(l)
	tr, freer := float32SliceToTypedArray(r)
	if buf.Get("copyToChannel") != js.Undefined() {
		buf.Call("copyToChannel", tl, 0, 0)
		buf.Call("copyToChannel", tr, 1, 0)
	} else {
		// copyToChannel is not defined on Safari 11
		buf.Call("getChannelData", 0).Call("set", tl)
		buf.Call("getChannelData", 1).Call("set", tr)
	}
	freel()
	freer()

	s := p.context.Call("createBufferSource")
	s.Set("buffer", buf)
	s.Call("connect", p.context.Get("destination"))
	s.Call("start", p.nextPos)
	p.nextPos += buf.Get("duration").Float()

	p.tmp = p.tmp[le:]
	return n, nil
}

func (p *driver) Close() error {
	for event, f := range p.callbacks {
		// https://developer.mozilla.org/en-US/docs/Web/API/EventTarget/removeEventListener
		// "Calling removeEventListener() with arguments that do not identify any currently registered EventListener on the EventTarget has no effect."
		js.Global().Get("document").Call("removeEventListener", event, f)
		f.Release()
	}
	p.callbacks = nil
	return nil
}
