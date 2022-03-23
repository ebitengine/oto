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
	"io"
	"reflect"
	"runtime"
	"sync"
	"syscall/js"
	"unsafe"
)

type context struct {
	audioContext js.Value
	ready        bool
	callbacks    map[string]js.Func

	sampleRate      int
	channelNum      int
	bitDepthInBytes int
	readBufferSize  int
}

func newContext(sampleRate int, channelNum int, bitDepthInBytes int) (*context, chan struct{}, error) {
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
		audioContext:    class.New(options),
		sampleRate:      sampleRate,
		channelNum:      channelNum,
		bitDepthInBytes: bitDepthInBytes,
	}

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

type player struct {
	context *context
	src     io.Reader
	eof     bool
	state   playerState
	gain    js.Value
	err     error
	buf     []byte

	f32L []float32
	f32R []float32

	nextPos           float64
	bufferSourceNodes []js.Value
	appendBufferFunc  js.Func

	cond *sync.Cond
}

func (c *context) NewPlayer(src io.Reader) Player {
	p := &player{
		context: c,
		src:     src,
		gain:    c.audioContext.Call("createGain"),
		cond:    sync.NewCond(&sync.Mutex{}),
	}
	p.appendBufferFunc = js.FuncOf(p.appendBuffer)
	p.gain.Call("connect", c.audioContext.Get("destination"))
	runtime.SetFinalizer(p, (*player).Close)
	return p
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

func (p *player) Play() {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()

	if p.err != nil {
		return
	}
	if p.state != playerPaused {
		return
	}

	buf := make([]byte, p.context.maxBufferSize())
	for len(p.buf) < p.context.maxBufferSize() {
		n, err := p.src.Read(buf)
		if err != nil && err != io.EOF {
			p.setErrorImpl(err)
			return
		}
		p.buf = append(p.buf, buf[:n]...)
		if err == io.EOF {
			p.eof = true
			break
		}
	}

	p.state = playerPlay
	p.appendBufferImpl(js.Undefined())
	p.appendBufferImpl(js.Undefined())

	go p.loop()
}

func (p *player) Pause() {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()
	p.pauseImpl()
}

func (p *player) pauseImpl() {
	if p.err != nil {
		return
	}
	if p.state != playerPlay {
		return
	}

	// Change the state first. appendBuffer is called as an 'ended' callback.
	var data [2][]float32
	for _, n := range p.bufferSourceNodes {
		for ch := 0; ch < 2; ch++ {
			t := n.Get("buffer").Call("getChannelData", ch)
			data[ch] = append(data[ch], float32ArrayToFloat32Slice(t)...)
		}
		n.Set("onended", nil)
		n.Call("stop")
		n.Call("disconnect")
	}

	bs := make([]byte, len(data[0])*4)
	fromLR(bs, data[0], data[1])
	p.buf = append(bs, p.buf...)
	p.state = playerPaused
	p.bufferSourceNodes = p.bufferSourceNodes[:0]
	p.nextPos = 0
	p.cond.Signal()
}

func (p *player) appendBuffer(this js.Value, args []js.Value) interface{} {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()

	p.appendBufferImpl(this)
	return nil
}

func (p *player) appendBufferImpl(audioBuffer js.Value) {
	// appendBuffer is called as the 'ended' callback of a buffer.
	// 'this' is an AudioBufferSourceNode that already finishes its playing.
	for i, n := range p.bufferSourceNodes {
		if n.Equal(audioBuffer) {
			p.bufferSourceNodes = append(p.bufferSourceNodes[:i], p.bufferSourceNodes[i+1:]...)
			break
		}
	}

	if p.state != playerPlay {
		return
	}

	if p.eof && len(p.buf) == 0 {
		if len(p.bufferSourceNodes) == 0 {
			p.pauseImpl()
		}
		return
	}

	c := p.context.audioContext.Get("currentTime").Float()
	if p.nextPos < c {
		// The exact current time might be too early. Add some delay on purpose to avoid buffer overlapping.
		p.nextPos = c + 1.0/60.0
	}

	bs := make([]byte, p.context.oneBufferSize())
	n := copy(bs, p.buf)
	copy(p.buf, p.buf[n:])
	p.buf = p.buf[:len(p.buf)-n]
	if len(p.buf) < p.context.maxBufferSize() {
		p.cond.Signal()
	}

	if len(bs) == 0 {
		// createBuffer fails with 0 bytes. Add some zeros instead.
		bs = make([]byte, 4096)
	}

	if cap(p.f32L) < len(bs)/4 {
		p.f32L = make([]float32, len(bs)/4)
	} else {
		p.f32L = p.f32L[:len(bs)/4]
	}
	if cap(p.f32R) < len(bs)/4 {
		p.f32R = make([]float32, len(bs)/4)
	} else {
		p.f32R = p.f32R[:len(bs)/4]
	}
	toLR(p.f32L, p.f32R, bs)
	tl, tr := float32SliceToTypedArray(p.f32L), float32SliceToTypedArray(p.f32R)

	buf := p.context.audioContext.Call("createBuffer", p.context.channelNum, len(bs)/p.context.channelNum/p.context.bitDepthInBytes, p.context.sampleRate)
	if buf.Get("copyToChannel").Truthy() {
		buf.Call("copyToChannel", tl, 0, 0)
		buf.Call("copyToChannel", tr, 1, 0)
	} else {
		// copyToChannel is not defined on Safari 11.
		buf.Call("getChannelData", 0).Call("set", tl)
		buf.Call("getChannelData", 1).Call("set", tr)
	}

	s := p.context.audioContext.Call("createBufferSource")
	s.Set("buffer", buf)
	s.Set("onended", p.appendBufferFunc)
	s.Call("connect", p.gain)
	s.Call("start", p.nextPos)
	p.nextPos += buf.Get("duration").Float()
	p.bufferSourceNodes = append(p.bufferSourceNodes, s)

	return
}

func (p *player) IsPlaying() bool {
	return p.state == playerPlay
}

func (p *player) Reset() {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()
	p.resetImpl()
}

func (p *player) resetImpl() {
	if p.err != nil {
		return
	}
	if p.state == playerClosed {
		return
	}

	p.pauseImpl()
	p.eof = false
	p.buf = p.buf[:0]
}

func (p *player) Volume() float64 {
	return p.gain.Get("gain").Get("value").Float()
}

func (p *player) SetVolume(volume float64) {
	p.gain.Get("gain").Set("value", volume)
}

func (p *player) UnplayedBufferSize() int {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()

	// This is not an accurate buffer size as part of the buffers might already be consumed.
	var sec float64
	for _, n := range p.bufferSourceNodes {
		sec += n.Get("buffer").Get("duration").Float()
	}
	return len(p.buf) + int(sec*float64(p.context.sampleRate*p.context.channelNum*p.context.bitDepthInBytes))
}

func (p *player) Err() error {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()
	return p.err
}

func (p *player) Close() error {
	runtime.SetFinalizer(p, nil)
	p.cond.L.Lock()
	defer p.cond.L.Unlock()
	return p.closeImpl()
}

func (p *player) closeImpl() error {
	p.resetImpl()
	p.state = playerClosed
	p.appendBufferFunc.Release()
	p.cond.Signal()
	return p.err
}

func (p *player) setError(err error) {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()
	p.setErrorImpl(err)
}

func (p *player) setErrorImpl(err error) {
	p.err = err
	p.closeImpl()
}

func (p *player) shouldWait() bool {
	switch p.state {
	case playerPaused:
		// Even when the player is paused, the loop immediately ends.
		// WebAudio doesn't have a notion of pause.
		return false
	case playerPlay:
		return len(p.buf) >= p.context.maxBufferSize()
	case playerClosed:
		return false
	default:
		panic("not reached")
	}
}

func (p *player) wait() bool {
	p.cond.L.Lock()
	defer p.cond.L.Unlock()

	for p.shouldWait() {
		p.cond.Wait()
	}
	return p.state == playerPlay && !p.eof
}

func (p *player) loop() {
	buf := make([]byte, 4096)
	for {
		if !p.wait() {
			return
		}

		n, err := p.src.Read(buf)
		if err != nil && err != io.EOF {
			p.setError(err)
			return
		}

		p.cond.L.Lock()
		p.buf = append(p.buf, buf[:n]...)
		if err == io.EOF {
			// p.eof can be true even if the buffer is not consumed yet.
			// This might be different from the other drivers.
			p.eof = true
			p.cond.L.Unlock()
			return
		}
		p.cond.L.Unlock()
	}
}

func toLR(l, r []float32, data []byte) {
	const max = 1 << 15

	for i := 0; i < len(data)/4; i++ {
		l[i] = float32(int16(data[4*i])|int16(data[4*i+1])<<8) / max
		r[i] = float32(int16(data[4*i+2])|int16(data[4*i+3])<<8) / max
	}
}

func fromLR(bs []byte, l, r []float32) {
	const max = 1 << 15

	if len(l) != len(r) {
		panic("oto: len(l) must equal to len(r) at fromLR")
	}

	for i := range l {
		lv := int16(l[i] * max)
		bs[4*i] = byte(lv)
		bs[4*i+1] = byte(lv >> 8)
		rv := int16(r[i] * max)
		bs[4*i+2] = byte(rv)
		bs[4*i+3] = byte(rv >> 8)
	}
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

func float32ArrayToFloat32Slice(v js.Value) []float32 {
	bs := make([]byte, v.Get("byteLength").Int())
	js.CopyBytesToGo(bs, js.Global().Get("Uint8Array").New(v.Get("buffer"), v.Get("byteOffset"), v.Get("byteLength")))

	h := (*reflect.SliceHeader)(unsafe.Pointer(&bs))
	h.Len /= 4
	h.Cap /= 4
	f32s := *(*[]float32)(unsafe.Pointer(h))
	runtime.KeepAlive(bs)

	return f32s
}
