package oto

// #cgo CFLAGS: -I/System/Library/Frameworks
// #cgo LDFLAGS: -framework CoreAudio -framework AudioToolbox -framework Foundation
// #include "player_darwin.h"
import "C"

// global buffer since it must be accessed from the go_input_callback
var Buf []byte

type player struct {
	audioPlayer C.AudioPlayer
	playing     bool
}

func (p *player) Write(b []byte) (n int, err error) {
	if !p.playing {
		p.playing = true
		p.StartPlayback()
	}
	Buf = append(Buf, b...)
	return len(b), nil
}

func (p *player) Close() error {
	C.ClosePlayer(&p.audioPlayer)
	return nil
}

func NewDarwinPlayer() (*player, error) {
	return newPlayer(0, 0, 0, 0)
}

func newPlayer(sampleRate, channelNum, bytesPerSample, bufferSizeInBytes int) (*player, error) {
	p := &player{}

	//bufferSize := bufferSizeInBytes / (channelNum * bytesPerSample)
	//buf = make([]byte, 0, bufferSize)
	p.audioPlayer = C.NewAudioPlayer(C.Float64(sampleRate), C.UInt32(channelNum), C.UInt32(bytesPerSample*8))
	return p, nil
}

func (p *player) StartPlayback() {
	C.StartPlayback(&p.audioPlayer)
}

func (p *player) stopPlayback() {
	p.playing = false
	C.StopPlayback(&p.audioPlayer)
}

// go_input_callback is responsible for placing information onto the underlying C circular buffer to be consumed by
// the go_output_callback, which will play audio to the underlying device

//export go_input_callback
func go_input_callback(player *C.AudioPlayer,
	flags *C.AudioUnitRenderActionFlags,
	audioTimeStamp *C.AudioTimeStamp,
	inBusNumber C.UInt32,
	inNumFrames C.UInt32,
	ioData *C.AudioBufferList) C.OSStatus {

	//j := player.startingFrameCount
	//cycleLength := float32(44100.0 / 440.0)
	f := 0
	if len(Buf) > 4 {
		for frame := C.UInt32(0); frame < inNumFrames-3; frame += 4 {
			if len(Buf) < f {
				Buf = make([]byte, 4)
			}
			//val := C.Float32(math.Sin(float64(2 * 3.14159265358979323846264338327950288 * (float32(j) / cycleLength))))

			// CAUTION - this currently just produces horrible loud noise, don't actually use it
			ch1 := C.UInt16(uint16(Buf[0])<<8 | uint16(Buf[1]))
			ch2 := C.UInt16(uint16(Buf[2])<<8 | uint16(Buf[3]))

			C.RenderUint16Data(0, frame, ch1, ioData)
			C.RenderUint16Data(1, frame, ch2, ioData)

			Buf = Buf[:3]
			//j++
			f += 4
		}
	} else {
		C.MakeBufferSilent(ioData)
	}

	//if len(Buf) > 0 {
	//	fmt.Println("Copying memory")
	//	C.MemCpyBuffer(unsafe.Pointer(&Buf[0]), ioData, inNumFrames/2)
	//	Buf = Buf[:inNumFrames/2]
	//} else {
	//	C.MakeBufferSilent(ioData)
	//}
	//player.startingFrameCount = j
	return C.noErr
}
