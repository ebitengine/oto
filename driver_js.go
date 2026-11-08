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
	"fmt"
	"runtime"
	"syscall/js"
	"time"
	"unsafe"

	"github.com/ebitengine/oto/v3/internal/mux"
)

type context struct {
	audioContext            js.Value
	scriptProcessor         js.Value
	scriptProcessorCallback js.Func
	err                     atomicError

	// outputTimestamp reports whether the AudioContext has getOutputTimestamp.
	outputTimestamp bool

	mux *mux.Mux
}

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, _ string) (*context, chan struct{}, error) {
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
		audioContext: class.New(options),
		mux:          mux.New(sampleRate, channelCount, format),
	}
	d.outputTimestamp = d.audioContext.Get("getOutputTimestamp").Truthy()

	if bufferSizeInBytes == 0 {
		// 4096 was not great at least on Safari 15.
		bufferSizeInBytes = 8192 * channelCount
	}

	buf32 := make([]float32, bufferSizeInBytes/4)
	var moduleReady bool
	var finishInitialization func()

	if w := d.audioContext.Get("audioWorklet"); w.Truthy() {
		script := fmt.Sprintf(`
class OtoWorkletProcessor extends AudioWorkletProcessor {
	constructor() {
		super();
		this.bufferSize_ = %[1]d;
		this.channelCount_ = %[2]d;
		this.buf_ = new Float32Array();
		this.waitRecv_ = false;
		// frames_ counts the frames of the mix output so far.
		this.frames_ = 0;
		// reportedTime_ is the context time of the last report to the main thread.
		this.reportedTime_ = -Infinity;

		// Receive data from the main thread.
		this.port.onmessage = (event) => {
			const buf = event.data;
			const newBuf = new Float32Array(this.buf_.length + buf.length);
			newBuf.set(this.buf_);
			newBuf.set(buf, this.buf_.length);
			this.buf_ = newBuf;
			this.waitRecv_ = false;
		}
	}

	process(inputs, outputs, parameters) {
		const output = outputs[0];

		// If the buffer is too short, request more data and return silence.
		if (this.buf_.length < output[0].length*this.channelCount_) {
			if (!this.waitRecv_) {
				this.waitRecv_ = true;
				this.port.postMessage(output[0].length);
			}
			for (let i = 0; i < output.length; i++) {
				output[i].fill(0);
			}
			// The silence delays the frames of the mix after it, so the last report no longer holds.
			if (this.reportedTime_ !== -Infinity) {
				this.reportedTime_ = -Infinity;
				this.port.postMessage({clear: true});
			}
			return true;
		}

		// Report which frame of the mix is output at which context time.
		if (currentTime - this.reportedTime_ >= %[3]g) {
			this.reportedTime_ = currentTime;
			this.port.postMessage({frame: this.frames_, time: currentTime});
		}

		// If the buffer is short, request more data.
		if (this.buf_.length <= Math.max(this.bufferSize_*this.channelCount_ / 2, output[0].length*this.channelCount_) && !this.waitRecv_) {
			this.waitRecv_ = true;
			this.port.postMessage(output[0].length);
		}

		for (let i = 0; i < this.channelCount_; i++) {
			for (let j = 0; j < output[i].length; j++) {
				output[i][j] = this.buf_[j*this.channelCount_+i];
			}
		}
		this.buf_ = this.buf_.slice(output[0].length*this.channelCount_);
		this.frames_ += output[0].length;
		return true;
	}
}
registerProcessor('oto-worklet-processor', OtoWorkletProcessor);
`, bufferSizeInBytes/4/channelCount, channelCount, workletReportInterval.Seconds())
		scriptURL := newScriptURL(script)
		var onAddModuleSuccess, onAddModuleFailure js.Func
		release := func() {
			js.Global().Get("URL").Call("revokeObjectURL", scriptURL)
			onAddModuleSuccess.Release()
			onAddModuleFailure.Release()
		}
		onAddModuleSuccess = js.FuncOf(func(this js.Value, arguments []js.Value) any {
			defer release()

			node := js.Global().Get("AudioWorkletNode").New(d.audioContext, "oto-worklet-processor", map[string]any{
				"outputChannelCount": []any{channelCount},
			})
			port := node.Get("port")
			// The worklet processor sends reports of the frame it outputs, and requests for more data.
			port.Set("onmessage", js.FuncOf(func(this js.Value, arguments []js.Value) any {
				data := arguments[0].Get("data")
				if data.Type() == js.TypeObject {
					if data.Get("clear").Truthy() {
						d.mux.ClearPlayed()
						return nil
					}
					d.reportPlayed(int64(data.Get("frame").Float()), data.Get("time").Float())
					return nil
				}

				// When the worklet processor requests more data, send the data to the worklet.
				// A transfer must cover at least one render quantum, even when the
				// requested context buffer is smaller than the worklet's output.
				if size := data.Int() * channelCount; len(buf32) < size {
					buf32 = make([]float32, size)
				}
				d.mux.ReadFloat32s(buf32)
				buf := float32SliceToTypedArray(buf32)
				port.Call("postMessage", buf, map[string]any{
					"transfer": []any{buf.Get("buffer")},
				})
				return nil
			}))
			node.Call("connect", d.audioContext.Get("destination"))

			moduleReady = true
			finishInitialization()
			return nil
		})
		onAddModuleFailure = js.FuncOf(func(this js.Value, arguments []js.Value) any {
			defer release()
			d.err.Join(fmt.Errorf("oto: AudioWorklet.addModule failed: %s", js.Global().Get("String").Invoke(arguments[0]).String()))
			finishInitialization()
			return nil
		})
		w.Call("addModule", scriptURL).Call("then", onAddModuleSuccess, onAddModuleFailure)
	} else {
		// Use ScriptProcessorNode if AudioWorklet is not available.

		chBuf32 := make([][]float32, channelCount)
		for i := range chBuf32 {
			chBuf32[i] = make([]float32, len(buf32)/channelCount)
		}

		// frames counts the frames of the mix read so far.
		var frames int64

		sp := d.audioContext.Call("createScriptProcessor", bufferSizeInBytes/4/channelCount, 0, channelCount)
		f := js.FuncOf(func(this js.Value, arguments []js.Value) any {
			// The buffer filled now is output at the context time playbackTime.
			if t := arguments[0].Get("playbackTime"); t.Type() == js.TypeNumber {
				d.reportPlayed(frames, t.Float())
			}
			d.mux.ReadFloat32s(buf32)
			frames += int64(len(buf32) / channelCount)
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
		moduleReady = true
	}

	// Browsers require user interaction to start the audio.
	// https://developers.google.com/web/updates/2017/09/autoplay-policy-changes#webaudio

	events := []string{"touchend", "keyup", "mouseup"}

	var resumed, resuming, finished bool
	var onEventFired js.Func
	finishInitialization = func() {
		if finished || (d.err.Load() == nil && (!moduleReady || !resumed)) {
			return
		}
		finished = true
		close(ready)
		for _, event := range events {
			js.Global().Get("document").Call("removeEventListener", event, onEventFired)
		}
		onEventFired.Release()
	}
	onEventFired = js.FuncOf(func(this js.Value, arguments []js.Value) any {
		if resumed || resuming || finished {
			return nil
		}
		resuming = true
		// A module load failure can finish initialization while resume is pending.
		// Keep these callbacks alive until their promise settles.
		var onSuccess, onFailure js.Func
		release := func() {
			resuming = false
			onSuccess.Release()
			onFailure.Release()
		}
		onSuccess = js.FuncOf(func(this js.Value, arguments []js.Value) any {
			defer release()
			resumed = true
			finishInitialization()
			return nil
		})
		onFailure = js.FuncOf(func(this js.Value, arguments []js.Value) any {
			defer release()
			return nil
		})
		d.audioContext.Call("resume").Call("then", onSuccess, onFailure)
		return nil
	})
	for _, event := range events {
		js.Global().Get("document").Call("addEventListener", event, onEventFired)
	}
	if d.audioContext.Get("state").String() == "running" {
		resumed = true
		finishInitialization()
	}

	return d, ready, nil
}

func (c *context) Suspend() error {
	c.audioContext.Call("suspend")
	c.mux.ClearPlayed()
	return nil
}

func (c *context) Resume() error {
	c.audioContext.Call("resume")
	return nil
}

func (c *context) Err() error {
	return c.err.Load()
}

// workletReportInterval is how often, in context time, the worklet reports which
// frame of the mix it outputs.
const workletReportInterval = 100 * time.Millisecond

// reportPlayed reports to the mux that frame of the mix is output at the context
// time contextTime, in seconds.
func (c *context) reportPlayed(frame int64, contextTime float64) {
	if !c.outputTimestamp {
		return
	}
	// getOutputTimestamp gives the context time of the frame the audio hardware
	// plays now, and the performance time it is played at.
	ts := c.audioContext.Call("getOutputTimestamp")
	performanceTime := ts.Get("performanceTime")
	// The performance time is 0 until the audio hardware starts playing.
	if performanceTime.Type() != js.TypeNumber || performanceTime.Float() == 0 {
		return
	}
	ms := performanceTime.Float() + (contextTime-ts.Get("contextTime").Float())*1000 - js.Global().Get("performance").Call("now").Float()
	c.mux.SetPlayed(frame, time.Now().Add(time.Duration(ms*float64(time.Millisecond))))
}

func float32SliceToTypedArray(s []float32) js.Value {
	bs := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
	a := js.Global().Get("Uint8Array").New(len(bs))
	js.CopyBytesToJS(a, bs)
	runtime.KeepAlive(s)
	buf := a.Get("buffer")
	return js.Global().Get("Float32Array").New(buf, a.Get("byteOffset"), a.Get("byteLength").Int()/4)
}

func newScriptURL(script string) js.Value {
	blob := js.Global().Get("Blob").New([]any{script}, map[string]any{"type": "text/javascript"})
	return js.Global().Get("URL").Call("createObjectURL", blob)
}
