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

//go:build !android && !darwin && !js && !windows
// +build !android,!darwin,!js,!windows

package oto

// #cgo pkg-config: alsa
//
// #include <alsa/asoundlib.h>
import "C"

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type context struct {
	sampleRate      int
	channelNum      int
	bitDepthInBytes int

	suspended bool

	handle        *C.snd_pcm_t
	supportsPause bool

	cond *sync.Cond

	players *players
	err     atomicError
}

var theContext *context

func alsaError(name string, err C.int) error {
	return fmt.Errorf("oto: ALSA error at %s: %s", name, C.GoString(C.snd_strerror(err)))
}

func deviceCandidates() []string {
	cmd := exec.Command("arecord", "-L")
	out, err := cmd.Output()
	if err != nil {
		return []string{"default"}
	}

	var devices []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		device := strings.TrimSpace(line)
		if device == "null" {
			continue
		}
		if device == "default" {
			continue
		}
		if device == "hw" || strings.HasPrefix(device, "hw:") {
			continue
		}
		devices = append(devices, device)
	}

	devices = append([]string{"default"}, devices...)
	return devices
}

func newContext(sampleRate, channelNum, bitDepthInBytes int) (*context, chan struct{}, error) {
	ready := make(chan struct{})
	close(ready)

	c := &context{
		sampleRate:      sampleRate,
		channelNum:      channelNum,
		bitDepthInBytes: bitDepthInBytes,
		cond:            sync.NewCond(&sync.Mutex{}),
		players:         newPlayers(),
	}
	theContext = c

	// Open a default ALSA audio device for blocking stream playback
	type openError struct {
		device string
		err    C.int
	}
	var openErrs []openError
	var found bool

	for _, name := range deviceCandidates() {
		cname := C.CString(name)
		defer C.free(unsafe.Pointer(cname))
		if err := C.snd_pcm_open(&c.handle, cname, C.SND_PCM_STREAM_PLAYBACK, 0); err < 0 {
			openErrs = append(openErrs, openError{
				device: name,
				err:    err,
			})
			continue
		}
		found = true
		break
	}
	if !found {
		var msgs []string
		for _, e := range openErrs {
			msgs = append(msgs, fmt.Sprintf("%q: %s", e.device, C.GoString(C.snd_strerror(e.err))))
		}
		return nil, nil, fmt.Errorf("oto: ALSA error at snd_pcm_open: %s", strings.Join(msgs, ", "))
	}

	periodSize := C.snd_pcm_uframes_t(1024)
	bufferSize := periodSize * 2
	if err := c.alsaPcmHwParams(sampleRate, channelNum, &bufferSize, &periodSize); err != nil {
		return nil, nil, err
	}

	go func() {
		buf32 := make([]float32, int(periodSize)*c.channelNum)
		for {
			if !c.readAndWrite(buf32) {
				return
			}
		}
	}()

	return c, ready, nil
}

func (c *context) alsaPcmHwParams(sampleRate, channelNum int, bufferSize, periodSize *C.snd_pcm_uframes_t) error {
	var params *C.snd_pcm_hw_params_t
	C.snd_pcm_hw_params_malloc(&params)
	defer C.free(unsafe.Pointer(params))

	if err := C.snd_pcm_hw_params_any(c.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params_any", err)
	}
	if err := C.snd_pcm_hw_params_set_access(c.handle, params, C.SND_PCM_ACCESS_RW_INTERLEAVED); err < 0 {
		return alsaError("snd_pcm_hw_params_set_access", err)
	}
	if err := C.snd_pcm_hw_params_set_format(c.handle, params, C.SND_PCM_FORMAT_FLOAT_LE); err < 0 {
		return alsaError("snd_pcm_hw_params_set_format", err)
	}
	if err := C.snd_pcm_hw_params_set_channels(c.handle, params, C.unsigned(channelNum)); err < 0 {
		return alsaError("snd_pcm_hw_params_set_channels", err)
	}
	if err := C.snd_pcm_hw_params_set_rate_resample(c.handle, params, 1); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_resample", err)
	}
	sr := C.unsigned(sampleRate)
	if err := C.snd_pcm_hw_params_set_rate_near(c.handle, params, &sr, nil); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_near", err)
	}
	if err := C.snd_pcm_hw_params_set_buffer_size_near(c.handle, params, bufferSize); err < 0 {
		return alsaError("snd_pcm_hw_params_set_buffer_size_near", err)
	}
	if err := C.snd_pcm_hw_params_set_period_size_near(c.handle, params, periodSize, nil); err < 0 {
		return alsaError("snd_pcm_hw_params_set_period_size_near", err)
	}
	if err := C.snd_pcm_hw_params(c.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params", err)
	}
	c.supportsPause = C.snd_pcm_hw_params_can_pause(params) == 1
	return nil
}

func (c *context) readAndWrite(buf32 []float32) bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.suspended && c.err.Load() == nil {
		c.cond.Wait()
	}
	if c.err.Load() != nil {
		return false
	}

	c.players.read(buf32)

	for len(buf32) > 0 {
		n := C.snd_pcm_writei(c.handle, unsafe.Pointer(&buf32[0]), C.snd_pcm_uframes_t(len(buf32)/c.channelNum))
		if n < 0 {
			n = C.long(C.snd_pcm_recover(c.handle, C.int(n), 1))
		}
		if n < 0 {
			c.err.TryStore(alsaError("snd_pcm_writei or snd_pcm_recover", C.int(n)))
			return false
		}
		buf32 = buf32[int(n)*c.channelNum:]
	}
	return true
}

func (c *context) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	c.suspended = true
	if c.supportsPause {
		if err := C.snd_pcm_pause(c.handle, 1); err < 0 {
			return alsaError("snd_pcm_pause", err)
		}
		return nil
	}

	if err := C.snd_pcm_drop(c.handle); err < 0 {
		return alsaError("snd_pcm_drop", err)
	}
	return nil
}

func (c *context) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err.(error)
	}

	defer func() {
		c.suspended = false
		c.cond.Signal()
	}()

	if c.supportsPause {
		if err := C.snd_pcm_pause(c.handle, 0); err < 0 {
			return alsaError("snd_pcm_pause", err)
		}
		return nil
	}

try:
	if err := C.snd_pcm_resume(c.handle); err < 0 {
		if err == -C.EAGAIN {
			time.Sleep(100 * time.Millisecond)
			goto try
		}
		if err == -C.ENOSYS {
			if err := C.snd_pcm_prepare(c.handle); err < 0 {
				return alsaError("snd_pcm_prepare", err)
			}
			return nil
		}
		return alsaError("snd_pcm_resume", err)
	}
	return nil
}

func (c *context) Err() error {
	if err := c.err.Load(); err != nil {
		return err.(error)
	}
	return nil
}
