// Copyright 2026 The Oto Authors
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

//go:build ((linux && !android) || freebsd || netbsd) && !nintendosdk && !playstation5

package oto

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/ebitengine/oto/v3/internal/mux"
)

const (
	_SND_PCM_STREAM_PLAYBACK       = 0
	_SND_PCM_FORMAT_FLOAT_LE       = 14
	_SND_PCM_ACCESS_RW_INTERLEAVED = 3
)

var (
	_snd_strerror                           func(errnum int32) string
	_snd_pcm_open                           func(pcm *uintptr, name string, stream int32, mode int32) int32
	_snd_pcm_close                          func(pcm uintptr) int32
	_snd_pcm_hw_params_malloc               func(ptr *uintptr) int32
	_snd_pcm_hw_params_free                 func(obj uintptr)
	_snd_pcm_hw_params_any                  func(pcm, params uintptr) int32
	_snd_pcm_hw_params_set_access           func(pcm, params uintptr, access uint32) int32
	_snd_pcm_hw_params_set_format           func(pcm, params uintptr, format int32) int32
	_snd_pcm_hw_params_set_channels         func(pcm, params uintptr, val uint32) int32
	_snd_pcm_hw_params_set_rate_resample    func(pcm, params uintptr, val uint32) int32
	_snd_pcm_hw_params_set_rate_near        func(pcm, params uintptr, val *uint32, dir *int32) int32
	_snd_pcm_hw_params_set_buffer_size_near func(pcm, params uintptr, val *uint) int32
	_snd_pcm_hw_params_set_period_size_near func(pcm, params uintptr, val *uint, dir *int32) int32
	_snd_pcm_hw_params                      func(pcm, params uintptr) int32
	_snd_pcm_writei                         func(pcm uintptr, buf []float32, size uint) int
	_snd_pcm_recover                        func(pcm uintptr, err int32, silent int32) int32

	_snd_device_name_hint      func(card int32, iface string, hints *unsafe.Pointer) int32
	_snd_device_name_free_hint func(hints unsafe.Pointer) int32
	_snd_device_name_get_hint  func(hint unsafe.Pointer, id string) unsafe.Pointer

	_free func(ptr unsafe.Pointer)
)

func init() {
	newALSAContext = func(sampleRate, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (unixBackend, error) {
		c, err := newALSAContextImpl(sampleRate, channelCount, mux, bufferSizeInBytes)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// loadALSA loads libasound and binds the functions above. A context is created at most once
// per process (see NewContext), so this runs at most once and needs no synchronization.
func loadALSA() error {
	var handle uintptr
	var err error
	for _, name := range []string{"libasound.so.2", "libasound.so"} {
		handle, err = purego.Dlopen(name, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err == nil {
			break
		}
	}
	if handle == 0 {
		return fmt.Errorf("oto: failed to load libasound: %w", err)
	}

	purego.RegisterLibFunc(&_snd_strerror, handle, "snd_strerror")
	purego.RegisterLibFunc(&_snd_pcm_open, handle, "snd_pcm_open")
	purego.RegisterLibFunc(&_snd_pcm_close, handle, "snd_pcm_close")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_malloc, handle, "snd_pcm_hw_params_malloc")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_free, handle, "snd_pcm_hw_params_free")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_any, handle, "snd_pcm_hw_params_any")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_access, handle, "snd_pcm_hw_params_set_access")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_format, handle, "snd_pcm_hw_params_set_format")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_channels, handle, "snd_pcm_hw_params_set_channels")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_rate_resample, handle, "snd_pcm_hw_params_set_rate_resample")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_rate_near, handle, "snd_pcm_hw_params_set_rate_near")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_buffer_size_near, handle, "snd_pcm_hw_params_set_buffer_size_near")
	purego.RegisterLibFunc(&_snd_pcm_hw_params_set_period_size_near, handle, "snd_pcm_hw_params_set_period_size_near")
	purego.RegisterLibFunc(&_snd_pcm_hw_params, handle, "snd_pcm_hw_params")
	purego.RegisterLibFunc(&_snd_pcm_writei, handle, "snd_pcm_writei")
	purego.RegisterLibFunc(&_snd_pcm_recover, handle, "snd_pcm_recover")
	purego.RegisterLibFunc(&_snd_device_name_hint, handle, "snd_device_name_hint")
	purego.RegisterLibFunc(&_snd_device_name_free_hint, handle, "snd_device_name_free_hint")
	purego.RegisterLibFunc(&_snd_device_name_get_hint, handle, "snd_device_name_get_hint")

	// libc free, for the strings snd_device_name_get_hint allocates. It resolves through the
	// libasound handle, whose dependency tree includes libc, so no OS-specific libc name is needed.
	purego.RegisterLibFunc(&_free, handle, "free")
	return nil
}

type alsaContext struct {
	channelCount int

	suspended bool

	handle uintptr

	cond *sync.Cond

	mux *mux.Mux
	err atomicError
}

func newALSAContextImpl(sampleRate int, channelCount int, mux *mux.Mux, bufferSizeInBytes int) (*alsaContext, error) {
	if channelCount != 1 && channelCount != 2 {
		return nil, fmt.Errorf("oto: ALSA backend supports only mono or stereo output: %d", channelCount)
	}
	if err := loadALSA(); err != nil {
		return nil, err
	}

	c := &alsaContext{
		channelCount: channelCount,
		cond:         sync.NewCond(&sync.Mutex{}),
		mux:          mux,
	}

	// Open a default ALSA audio device for blocking stream playback.
	var openErrs []string
	var handle uintptr
	var found bool
	for _, name := range deviceCandidates() {
		if err := _snd_pcm_open(&handle, name, _SND_PCM_STREAM_PLAYBACK, 0); err < 0 {
			openErrs = append(openErrs, fmt.Sprintf("%q: %s", name, _snd_strerror(err)))
			continue
		}
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("oto: ALSA error at snd_pcm_open: %s", strings.Join(openErrs, ", "))
	}
	c.handle = handle

	// TODO: Should snd_pcm_hw_params_set_periods be called explicitly?
	const periods = 2
	var periodSize uint
	if bufferSizeInBytes != 0 {
		periodSize = uint(bufferSizeInBytes / (channelCount * 4 * periods))
		if periodSize == 0 {
			periodSize = 1
		}
	} else {
		periodSize = 1024
	}
	bufferSize := periodSize * periods
	if err := c.alsaPCMHwParams(sampleRate, channelCount, &bufferSize, &periodSize); err != nil {
		_snd_pcm_close(c.handle)
		return nil, err
	}

	go func() {
		// The loop only returns when readAndWrite hits a permanent error, so close the
		// handle here to avoid leaking it after a terminal audio failure.
		defer _snd_pcm_close(c.handle)
		buf32 := make([]float32, int(periodSize)*channelCount)
		for {
			if !c.readAndWrite(buf32) {
				return
			}
		}
	}()

	return c, nil
}

func (c *alsaContext) alsaPCMHwParams(sampleRate, channelCount int, bufferSize, periodSize *uint) error {
	var params uintptr
	if err := _snd_pcm_hw_params_malloc(&params); err < 0 {
		return alsaError("snd_pcm_hw_params_malloc", err)
	}
	defer _snd_pcm_hw_params_free(params)

	if err := _snd_pcm_hw_params_any(c.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params_any", err)
	}
	if err := _snd_pcm_hw_params_set_access(c.handle, params, _SND_PCM_ACCESS_RW_INTERLEAVED); err < 0 {
		return alsaError("snd_pcm_hw_params_set_access", err)
	}
	if err := _snd_pcm_hw_params_set_format(c.handle, params, _SND_PCM_FORMAT_FLOAT_LE); err < 0 {
		return alsaError("snd_pcm_hw_params_set_format", err)
	}
	if err := _snd_pcm_hw_params_set_channels(c.handle, params, uint32(channelCount)); err < 0 {
		return alsaError("snd_pcm_hw_params_set_channels", err)
	}
	if err := _snd_pcm_hw_params_set_rate_resample(c.handle, params, 1); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_resample", err)
	}
	sr := uint32(sampleRate)
	if err := _snd_pcm_hw_params_set_rate_near(c.handle, params, &sr, nil); err < 0 {
		return alsaError("snd_pcm_hw_params_set_rate_near", err)
	}
	if err := _snd_pcm_hw_params_set_buffer_size_near(c.handle, params, bufferSize); err < 0 {
		return alsaError("snd_pcm_hw_params_set_buffer_size_near", err)
	}
	if err := _snd_pcm_hw_params_set_period_size_near(c.handle, params, periodSize, nil); err < 0 {
		return alsaError("snd_pcm_hw_params_set_period_size_near", err)
	}
	if err := _snd_pcm_hw_params(c.handle, params); err < 0 {
		return alsaError("snd_pcm_hw_params", err)
	}
	return nil
}

func (c *alsaContext) readAndWrite(buf32 []float32) bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.suspended && c.err.Load() == nil {
		c.cond.Wait()
	}
	if c.err.Load() != nil {
		return false
	}

	c.mux.ReadFloat32s(buf32)

	buf := buf32
	for len(buf) > 0 {
		n := _snd_pcm_writei(c.handle, buf, uint(len(buf)/c.channelCount))
		if n < 0 {
			n = int(_snd_pcm_recover(c.handle, int32(n), 1))
		}
		if n < 0 {
			c.err.Join(alsaError("snd_pcm_writei or snd_pcm_recover", int32(n)))
			return false
		}
		buf = buf[n*c.channelCount:]
	}
	return true
}

func (c *alsaContext) Suspend() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err
	}

	c.suspended = true

	// Do not use snd_pcm_pause as not all devices support this.
	// Do not use snd_pcm_drop as this might hang (https://github.com/libsdl-org/SDL/blob/a5c610b0a3857d3138f3f3da1f6dc3172c5ea4a8/src/audio/alsa/SDL_alsa_audio.c#L478).
	return nil
}

func (c *alsaContext) Resume() error {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if err := c.err.Load(); err != nil {
		return err
	}

	c.suspended = false
	c.cond.Signal()
	return nil
}

func (c *alsaContext) Err() error {
	return c.err.Load()
}

func alsaError(name string, errno int32) error {
	return fmt.Errorf("oto: ALSA error at %s: %s", name, _snd_strerror(errno))
}

func deviceCandidates() []string {
	const getAllDevices = -1

	var hints unsafe.Pointer
	if _snd_device_name_hint(getAllDevices, "pcm", &hints) != 0 {
		return []string{"default", "plug:default"}
	}
	defer _snd_device_name_free_hint(hints)

	var devices []string
	ptrSize := unsafe.Sizeof(uintptr(0))
	for i := uintptr(0); ; i++ {
		hint := *(*unsafe.Pointer)(unsafe.Pointer(uintptr(hints) + i*ptrSize))
		if hint == nil {
			break
		}

		if hintString(hint, "IOID") == "Input" {
			continue
		}

		name := hintString(hint, "NAME")
		switch name {
		case "", "null", "default":
			continue
		}
		devices = append(devices, name)
	}

	return append([]string{"default", "plug:default"}, devices...)
}

// hintString returns the value of the given hint key as a Go string, freeing the C string that
// libasound allocated for it.
func hintString(hint unsafe.Pointer, id string) string {
	p := _snd_device_name_get_hint(hint, id)
	if p == nil {
		return ""
	}
	s := goString(p)
	_free(p)
	return s
}

// goString copies a NUL-terminated C string into a Go string.
func goString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	var bs []byte
	for i := uintptr(0); ; i++ {
		b := *(*byte)(unsafe.Pointer(uintptr(p) + i))
		if b == 0 {
			break
		}
		bs = append(bs, b)
	}
	return string(bs)
}
