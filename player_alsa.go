// +build linux
// +build !js
// +build !android
// +build !ios

package oto

// #cgo linux LDFLAGS: -lasound
// #include <alsa/asoundlib.h>
// #include "player_alsa.h"
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

type player struct {
	handle     *C.snd_pcm_t
	buf        []byte
	bufSamples int
}

func alsaError(err C.int) error {
	return errors.New(C.GoString(C.snd_strerror(err)))
}

func newPlayer(sampleRate, numChans, bytesPerSample, bufferSizeInBytes int) (*player, error) {
	var p player

	// open a default ALSA audio device for blocking stream playback
	errCode := C.snd_pcm_open(&p.handle, C.CString("default"), C.SND_PCM_STREAM_PLAYBACK, 0)
	if errCode < 0 {
		return nil, alsaError(errCode)
	}

	var (
		// sample format, either SND_PCM_FORMAT_S8 or SND_PCM_FORMAT_S16_LE
		format C.snd_pcm_format_t
		// bufferSize is the total size of the main circular buffer fullness of this buffer
		// oscilates somewhere between bufferSize and bufferSize-periodSize
		bufferSize = C.snd_pcm_uframes_t(bufferSizeInBytes / (numChans * bytesPerSample))
		// periodSize is the number of samples that will be taken from the main circular
		// buffer at once, we leave this value to bufferSize, because ALSA will change that
		// to the maximum viable number, obviously lower than bufferSize
		periodSize = bufferSize
	)

	// choose th correct sample format according to bytesPerSamples
	switch bytesPerSample {
	case 1:
		format = C.SND_PCM_FORMAT_S8
	case 2:
		format = C.SND_PCM_FORMAT_S16_LE
	default:
		panic(fmt.Errorf("oto: bytesPerSample can be 1 or 2, got %d", bytesPerSample))
	}

	// set the device hardware parameters according to sampleRate, numChans, format, bufferSize
	// and periodSize
	//
	// bufferSize and periodSize are passed as pointers, because they may be changed according
	// to the wisdom of ALSA
	//
	// ALSA will try too keep them as close to what was requested as possible
	errCode = C.ALSA_hw_params(p.handle, C.uint(sampleRate), C.uint(numChans), format, &bufferSize, &periodSize)
	if errCode < 0 {
		p.Close()
		return nil, alsaError(errCode)
	}
	fmt.Println(bufferSize, periodSize)

	// allocate the buffer of the size of the period, use the periodSize that we've got back
	// from ALSA after it's wise decision
	p.bufSamples = int(periodSize)
	p.buf = make([]byte, 0, p.bufSamples*numChans*bytesPerSample)

	return &p, nil
}

func (p *player) Write(data []byte) (n int, err error) {
	for len(data) > 0 {
		// cap(p.buf) is equal to the size of the period
		toWrite := min(len(data), cap(p.buf)-len(p.buf))
		p.buf = append(p.buf, data[:toWrite]...)
		data = data[toWrite:]
		n += toWrite

		// when our buffer is full, we flush it to ALSA
		if len(p.buf) == cap(p.buf) {
			// write samples to the main circular buffer
			wrote := C.snd_pcm_writei(p.handle, unsafe.Pointer(&p.buf[0]), C.snd_pcm_uframes_t(p.bufSamples))
			switch {
			case wrote == -C.EPIPE:
				// underrun, this means that the we send data too slow and need to
				// catch up
				//
				// when underrun occurs, sample processing stops, so we need to
				// rewoke it by snd_pcm_prepare
				C.snd_pcm_prepare(p.handle)
			case wrote < 0:
				// an error occured while writing samples
				return n, alsaError(C.int(wrote))
			}
			p.buf = p.buf[:0]
		}
	}
	return n, nil
}

func (p *player) Close() error {
	// drop the remaining unprocessed samples in the main circular buffer
	errCode := C.snd_pcm_drop(p.handle)
	if errCode < 0 {
		return alsaError(errCode)
	}
	errCode = C.snd_pcm_close(p.handle)
	if errCode < 0 {
		return alsaError(errCode)
	}
	return nil
}
