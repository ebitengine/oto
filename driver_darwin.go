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
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/oto/v3/internal/mux"
)

const (
	float32SizeInBytes = 4

	bufferCount = 4

	noErr = 0
)

// audioQueueNewOutputError retains the status so a rebuild can distinguish
// temporary audio-session failures from failures allocating or disposing buffers.
type audioQueueNewOutputError uintptr

func (e audioQueueNewOutputError) Error() string {
	return fmt.Sprintf("oto: AudioQueueNewOutput failed: %d", uintptr(e))
}

func newAudioQueue(sampleRate, channelCount int, oneBufferSizeInBytes int) (_AudioQueueRef, []_AudioQueueBufferRef, error) {
	desc := _AudioStreamBasicDescription{
		mSampleRate:       float64(sampleRate),
		mFormatID:         uint32(kAudioFormatLinearPCM),
		mFormatFlags:      uint32(kAudioFormatFlagIsFloat),
		mBytesPerPacket:   uint32(channelCount * float32SizeInBytes),
		mFramesPerPacket:  1,
		mBytesPerFrame:    uint32(channelCount * float32SizeInBytes),
		mChannelsPerFrame: uint32(channelCount),
		mBitsPerChannel:   uint32(8 * float32SizeInBytes),
	}

	var audioQueue _AudioQueueRef
	if osstatus := _AudioQueueNewOutput(
		&desc,
		render,
		nil,
		0, // CFRunLoopRef
		0, // CFStringRef
		0,
		&audioQueue); osstatus != noErr {
		return 0, nil, audioQueueNewOutputError(osstatus)
	}

	bufs := make([]_AudioQueueBufferRef, 0, bufferCount)
	for len(bufs) < cap(bufs) {
		var buf _AudioQueueBufferRef
		if osstatus := _AudioQueueAllocateBuffer(audioQueue, uint32(oneBufferSizeInBytes), &buf); osstatus != noErr {
			// Disposing the queue also frees the buffers allocated so far.
			_ = _AudioQueueDispose(audioQueue, true)
			return 0, nil, fmt.Errorf("oto: AudioQueueAllocateBuffer failed: %d", osstatus)
		}
		buf.mAudioDataByteSize = uint32(oneBufferSizeInBytes)
		// mUserData holds the count of the frames enqueued up to the end of the buffer, and 0 until it is enqueued.
		buf.mUserData = 0
		bufs = append(bufs, buf)
	}

	return audioQueue, bufs, nil
}

// queueState is the actual state of the AudioQueue.
type queueState int

const (
	// queueStateStopped indicates that the AudioQueue is not running and a start attempt
	// may be made at any time.
	queueStateStopped queueState = iota

	// queueStateStartDeferred indicates that a start attempt failed with a temporary
	// error and the next attempt waits for a timer (deferStart) or an audio session
	// notification.
	queueStateStartDeferred

	// queueStateRunning indicates that the AudioQueue was started and has not been
	// paused or invalidated since.
	queueStateRunning
)

type context struct {
	audioQueue      _AudioQueueRef
	unqueuedBuffers []_AudioQueueBufferRef

	sampleRate           int
	channelCount         int
	oneBufferSizeInBytes int

	cond *sync.Cond

	// toSuspend indicates that Suspend was requested and the AudioQueue must not
	// run until Resume is requested. This is the desired state requested by the user,
	// and is independent of state, the actual state of the queue.
	//
	// This is atomic so that Suspend and Resume can record the requested state without
	// waiting for c.cond.L, which loop can hold for a long time across AudioToolbox
	// calls. Consecutive calls then take effect in the order they were made.
	toSuspend atomic.Bool

	// state is the actual state of the AudioQueue.
	state queueState

	// toRebuildQueue indicates that the AudioQueue was invalidated and must be
	// recreated before the next start. This concerns the validity of the queue object
	// and is independent of state, which concerns the start/stop lifecycle.
	toRebuildQueue bool

	// startRetries is the number of consecutive start attempts that failed with a
	// temporary error.
	startRetries int

	// startRetryTimer is the pending timer scheduled by deferStart, or nil. It is
	// stopped and cleared when the deferral is ended by another path (endDeferStart).
	startRetryTimer *time.Timer

	// readFrames counts the frames read from mux, which is the index of the frame read next.
	readFrames int64

	// enqueuedFrames counts the frames enqueued to audioQueue.
	enqueuedFrames int64

	// timelineOrigin is the count of the frames enqueued to audioQueue at the start of the queue's timeline,
	// where its sample time is 0.
	timelineOrigin int64

	// reportDue indicates that loop is to report to mux when the frames are played.
	reportDue bool

	// latency is what outputLatency returned at latencyQueriedAt.
	latency          time.Duration
	latencyQueriedAt time.Time

	mux *mux.Mux
	err atomicError
}

// TODO: Convert the error code correctly.
// See https://stackoverflow.com/questions/2196869/how-do-you-convert-an-iphone-osstatus-code-to-something-useful

// outputLatencyLifetime is how long a latency queried with outputLatency is used.
const outputLatencyLifetime = time.Second

// reportInterval is how often loop reports to mux when the frames are played.
const reportInterval = 100 * time.Millisecond

// maxReturnLead is the longest a buffer comes back before the end of its frames is output.
const maxReturnLead = 100 * time.Millisecond

// timeStampValid is the flags of an AudioTimeStamp of a playing queue.
const timeStampValid = kAudioTimeStampSampleTimeValid | kAudioTimeStampHostTimeValid

var theContext *context

func newContext(sampleRate int, channelCount int, format mux.Format, bufferSizeInBytes int, _ string) (*context, chan struct{}, error) {
	// defaultOneBufferSizeInBytes is the default buffer size in bytes.
	//
	// 12288 seems necessary at least on iPod touch (7th) and MacBook Pro 2020.
	// With 48000[Hz] stereo, the maximum delay is (12288*4[buffers] / 4 / 2)[frames] / 48000 [Hz] = 128[ms].
	// '4' is float32 size in bytes. '2' is a number of channels for stereo.
	const defaultOneBufferSizeInBytes = 12288

	var oneBufferSizeInBytes int
	if bufferSizeInBytes != 0 {
		oneBufferSizeInBytes = bufferSizeInBytes / bufferCount
	} else {
		oneBufferSizeInBytes = defaultOneBufferSizeInBytes
	}
	bytesPerSample := channelCount * 4
	oneBufferSizeInBytes = oneBufferSizeInBytes / bytesPerSample * bytesPerSample

	ready := make(chan struct{})

	c := &context{
		cond:                 sync.NewCond(&sync.Mutex{}),
		mux:                  mux.New(sampleRate, channelCount, format),
		sampleRate:           sampleRate,
		channelCount:         channelCount,
		oneBufferSizeInBytes: oneBufferSizeInBytes,
	}
	theContext = c

	if err := initializeAPI(); err != nil {
		return nil, nil, err
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		q, bs, err := newAudioQueue(c.sampleRate, c.channelCount, c.oneBufferSizeInBytes)
		if err != nil {
			c.err.Join(err)
			close(ready)
			return
		}
		c.initialize(q, bs)

		setupSessionNotifications()

		close(ready)

		go c.requestReports()
		c.loop()
	}()

	return c, ready, nil
}

// initialize sets the freshly created AudioQueue and attempts the first start unless
// a suspend is already requested.
// A temporary start failure is not an error: the start is deferred and retried by loop.
func (c *context) initialize(q _AudioQueueRef, bs []_AudioQueueBufferRef) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	c.audioQueue = q
	c.unqueuedBuffers = bs
	if !c.toSuspend.Load() {
		c.start()
	}
}

func (c *context) wait() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	for c.idle() && c.err.Load() == nil {
		c.cond.Wait()
	}
	return c.err.Load() == nil
}

// idle reports whether step has nothing to do for now: the actual queue state matches
// the requested state, no buffer is waiting to be filled, and no report is due. It must be
// kept consistent with step: whenever idle returns false, step must change some state.
// The caller must hold c.cond.L.
func (c *context) idle() bool {
	if c.toRebuildQueue {
		return c.state == queueStateStartDeferred
	}
	if c.toSuspend.Load() {
		return c.state != queueStateRunning
	}
	switch c.state {
	case queueStateStopped:
		return false
	case queueStateRunning:
		return len(c.unqueuedBuffers) == 0 && !c.reportDue
	default:
		return true
	}
}

func (c *context) loop() {
	buf32 := make([]float32, c.oneBufferSizeInBytes/4)
	for {
		if !c.wait() {
			return
		}
		c.step(buf32)
	}
}

func (c *context) step(buf32 []float32) {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if c.err.Load() != nil {
		return
	}

	if c.toRebuildQueue {
		if c.state == queueStateStartDeferred {
			return
		}
		if err := c.rebuildAudioQueue(); err != nil {
			var creationError audioQueueNewOutputError
			if errors.As(err, &creationError) && temporaryAudioSessionError(uintptr(creationError)) {
				c.deferStart()
				return
			}
			c.err.Join(err)
			return
		}
		c.toRebuildQueue = false
		if c.state == queueStateRunning {
			c.state = queueStateStopped
		}
		return
	}

	if c.toSuspend.Load() {
		if c.state == queueStateRunning {
			if err := c.pause(); err != nil {
				c.err.Join(err)
			}
		}
		return
	}

	switch c.state {
	case queueStateStopped:
		c.start()
		return
	case queueStateStartDeferred:
		return
	}

	if c.reportDue {
		c.reportDue = false
		c.reportPlayed()
	}

	if len(c.unqueuedBuffers) == 0 {
		return
	}
	c.enqueueBuffer(buf32)
}

// takeUnqueuedBuffer removes the first unqueued buffer and returns it.
// The caller must hold c.cond.L.
func (c *context) takeUnqueuedBuffer() _AudioQueueBufferRef {
	buf := c.unqueuedBuffers[0]
	copy(c.unqueuedBuffers, c.unqueuedBuffers[1:])
	c.unqueuedBuffers = c.unqueuedBuffers[:len(c.unqueuedBuffers)-1]
	return buf
}

// enqueueBuffer fills an unqueued buffer with the data read from mux and enqueues it.
// The caller must hold c.cond.L.
func (c *context) enqueueBuffer(buf32 []float32) {
	buf := c.takeUnqueuedBuffer()
	if end := int64(buf.mUserData); end != 0 {
		c.alignTimeline(end)
	}

	c.mux.ReadFloat32s(buf32)
	frames := int64(len(buf32) / c.channelCount)
	c.readFrames += frames
	copy(unsafe.Slice((*float32)(unsafe.Pointer(buf.mAudioData)), len(buf32)), buf32)
	c.enqueue(buf, frames)
}

// enqueuePrimer enqueues one silent frame, and reports whether it is enqueued.
// The caller must hold c.cond.L.
func (c *context) enqueuePrimer() bool {
	buf := c.takeUnqueuedBuffer()
	clear(unsafe.Slice((*float32)(unsafe.Pointer(buf.mAudioData)), c.channelCount))
	return c.enqueue(buf, 1)
}

// enqueue enqueues the first frames frames of buf, and reports whether it is enqueued.
// The caller must hold c.cond.L.
func (c *context) enqueue(buf _AudioQueueBufferRef, frames int64) bool {
	buf.mAudioDataByteSize = uint32(frames) * uint32(c.channelCount*float32SizeInBytes)
	buf.mUserData = uintptr(c.enqueuedFrames + frames)
	if osstatus := _AudioQueueEnqueueBuffer(c.audioQueue, buf, 0, nil); osstatus != noErr {
		if osstatus == kAudioQueueErr_QueueInvalidated {
			// The queue was invalidated (typically a mediaserverd reset).
			// The audio just rendered into `buf` is dropped: at most one buffer of glitch.
			c.state = queueStateStopped
			c.toRebuildQueue = true
			return false
		}
		c.err.Join(fmt.Errorf("oto: AudioQueueEnqueueBuffer failed: %d", osstatus))
		return false
	}
	c.enqueuedFrames += frames
	return true
}

// alignTimeline moves the origin of the queue's timeline to a buffer that came back long before its place on it.
// end is the count of the frames enqueued up to the end of the buffer.
// The caller must hold c.cond.L.
func (c *context) alignTimeline(end int64) {
	var ts _AudioTimeStamp
	if osstatus := _AudioQueueGetCurrentTime(c.audioQueue, 0, &ts, 0); osstatus != noErr || ts.mFlags&timeStampValid != timeStampValid {
		return
	}
	// A buffer comes back as the queue takes its frames for output, shortly before they are output, or after
	// its place if its frames are skipped. One that comes back long before means the timeline started over
	// while the frames were enqueued, and its frames are output about now.
	if float64(end-c.timelineOrigin)-ts.mSampleTime <= maxReturnLead.Seconds()*float64(c.sampleRate) {
		return
	}
	c.timelineOrigin = end - int64(ts.mSampleTime)
	// The last report is from the timeline before.
	c.mux.ClearPlayed()
}

// reportPlayed reports to mux when the frame read from it next is played.
// The caller must hold c.cond.L.
func (c *context) reportPlayed() {
	// A queue that is not playing has no current time, or a current time without a host time.
	var ts _AudioTimeStamp
	if osstatus := _AudioQueueGetCurrentTime(c.audioQueue, 0, &ts, 0); osstatus != noErr || ts.mFlags&timeStampValid != timeStampValid {
		c.mux.ClearPlayed()
		return
	}

	if now := time.Now(); now.Sub(c.latencyQueriedAt) >= outputLatencyLifetime {
		c.latency = outputLatency()
		c.latencyQueriedAt = now
	}

	// The frames enqueued are contiguous on the queue's timeline, from timelineOrigin at its start: a frame
	// enqueued too late for its place is skipped. So the frame read next is output when the timeline reaches
	// its place, and is played the output latency later.
	untilOutput := (float64(c.enqueuedFrames-c.timelineOrigin) - ts.mSampleTime) / float64(c.sampleRate)
	at := hostTimeToTime(ts.mHostTime).Add(time.Duration(untilOutput*float64(time.Second)) + c.latency)
	c.mux.SetPlayed(c.readFrames, at)
}

// requestReports makes loop report to mux when the frames are played every reportInterval, until the
// context fails.
func (c *context) requestReports() {
	t := time.NewTicker(reportInterval)
	defer t.Stop()

	for range t.C {
		if !c.requestReport() {
			return
		}
	}
}

// requestReport makes loop report to mux when the frames are played, and reports whether the context
// has not failed.
func (c *context) requestReport() bool {
	c.cond.L.Lock()
	defer c.cond.L.Unlock()

	if c.err.Load() != nil {
		c.mux.ClearPlayed()
		return false
	}
	c.reportDue = true
	c.cond.Signal()
	return true
}

// Suspend returns immediately. The actual AudioQueuePause runs on a
// background goroutine so the calling thread (typically the platform UI
// thread) never blocks on AudioToolbox calls. Errors from the asynchronous
// transition surface via Err.
func (c *context) Suspend() error {
	err := c.err.Load()
	c.toSuspend.Store(true)
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		c.cond.Signal()
	}()
	return err
}

// Resume returns immediately. See Suspend for the rationale; AudioQueueStart
// runs on a background goroutine.
func (c *context) Resume() error {
	err := c.err.Load()
	c.toSuspend.Store(false)
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		// Attempt the start right away even if a retry is pending with a backoff.
		// These two only affect the timing of the next start attempt, so running them
		// out of order with respect to another Suspend or Resume is harmless.
		c.endDeferStart()
		c.startRetries = 0
		c.cond.Signal()
	}()
	return err
}

// pause pauses the AudioQueue and updates c.state.
// The caller must hold c.cond.L.
func (c *context) pause() error {
	// Nothing is played until the queue starts again.
	c.mux.ClearPlayed()
	if osstatus := _AudioQueuePause(c.audioQueue); osstatus != noErr {
		if osstatus == kAudioQueueErr_QueueInvalidated {
			c.state = queueStateStopped
			c.toRebuildQueue = true
			return nil
		}
		return fmt.Errorf("oto: AudioQueuePause failed: %d", osstatus)
	}
	c.state = queueStateStopped
	return nil
}

// start attempts to start the AudioQueue once.
//
// On success, c.state becomes queueStateRunning. When the start fails with a temporary error, such
// as an audio session that cannot be activated because the application is in the
// background, another application owns the audio session, or media services are
// restarting, the start is deferred: playback stays silent and the attempt is repeated
// until it succeeds (#285). Any other failure is fatal and recorded in c.err.
//
// The caller must hold c.cond.L.
func (c *context) start() {
	// Enqueued before the queue starts, a frame is at the start of the queue's timeline, and the frames
	// enqueued after it follow it there.
	if c.enqueuedFrames == 0 && !c.enqueuePrimer() {
		return
	}
	osstatus := _AudioQueueStart(c.audioQueue, nil)
	if osstatus == noErr {
		c.state = queueStateRunning
		c.startRetries = 0
		return
	}

	switch {
	case osstatus == kAudioQueueErr_QueueInvalidated:
		// The queue died (typically a mediaserverd reset). Recreate it before the next
		// attempt.
		c.toRebuildQueue = true
		c.deferStart()
	case temporaryAudioSessionError(osstatus):
		// The audio session cannot be activated now. This state can last arbitrarily
		// long (e.g. as long as the application stays in the background), so no retry
		// limit applies.
		c.deferStart()
	default:
		c.err.Join(fmt.Errorf("oto: AudioQueueStart failed: %d", osstatus))
	}
}

// temporaryAudioSessionError reports statuses already treated as retryable when
// starting playback. These can also prevent creating a replacement queue.
func temporaryAudioSessionError(status uintptr) bool {
	switch status {
	case avAudioSessionErrorCodeCannotStartPlaying,
		avAudioSessionErrorCodeCannotInterruptOthers,
		avAudioSessionErrorCodeSiriIsRecording,
		avAudioSessionErrorCodeUnspecified,
		kAudioHardwareIllegalOperationError:
		return true
	default:
		return false
	}
}

// deferStart schedules the next start attempt after a backoff delay. Ending the
// deferral earlier, e.g. on an audio session notification, is done via endDeferStart.
// The caller must hold c.cond.L.
func (c *context) deferStart() {
	c.state = queueStateStartDeferred
	// Nothing is played until a start succeeds.
	c.mux.ClearPlayed()
	d := startRetryDelay(c.startRetries)
	c.startRetries++
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()
		if c.startRetryTimer != t {
			// The deferral this timer belonged to was already ended by endDeferStart.
			return
		}
		c.startRetryTimer = nil
		if c.state == queueStateStartDeferred {
			c.state = queueStateStopped
		}
		c.cond.Signal()
	})
	c.startRetryTimer = t
}

// endDeferStart ends a pending deferral, if any: the timer scheduled by deferStart is
// stopped and the state goes back to queueStateStopped so that the next start attempt
// can happen immediately.
// The caller must hold c.cond.L, and must call c.cond.Signal after completing all of
// its state changes.
func (c *context) endDeferStart() {
	if c.startRetryTimer != nil {
		c.startRetryTimer.Stop()
		c.startRetryTimer = nil
	}
	if c.state == queueStateStartDeferred {
		c.state = queueStateStopped
	}
}

// restartFromNotification requests an immediate start attempt in response to an audio
// session notification: any backoff pending from deferStart is canceled and the retry
// counter is reset. rebuild indicates that the AudioQueue must be recreated first.
//
// It returns immediately: an observer runs on the thread that posts the notification,
// the main thread for UIApplicationDidBecomeActiveNotification, and loop holds the
// lock across AudioToolbox calls.
func (c *context) restartFromNotification(rebuild bool) {
	go func() {
		c.cond.L.Lock()
		defer c.cond.L.Unlock()

		if rebuild {
			c.toRebuildQueue = true
		}
		c.endDeferStart()
		if !c.toSuspend.Load() {
			// An interruption stops the queue without notifying its owner, and whether
			// it is still running cannot be queried, so let loop start it again. A
			// start on a running queue returns noErr.
			c.state = queueStateStopped
		}
		c.startRetries = 0
		c.cond.Signal()
	}()
}

// rebuildAudioQueue disposes the current AudioQueue (which may already be invalid)
// and creates a fresh queue with new buffers. The new queue is left in the stopped
// state.
//
// The caller must hold c.cond.L.
func (c *context) rebuildAudioQueue() error {
	// The data the current queue holds is never played.
	c.mux.ClearPlayed()
	if c.audioQueue != 0 {
		// kAudioQueueErr_QueueInvalidated is expected here: that is the very case
		// being recovered from. Anything else is unexpected and worth surfacing.
		osstatus := _AudioQueueDispose(c.audioQueue, true)
		c.audioQueue = 0
		if osstatus != noErr && osstatus != kAudioQueueErr_QueueInvalidated {
			c.unqueuedBuffers = nil
			return fmt.Errorf("oto: AudioQueueDispose failed during rebuild: %d", osstatus)
		}
	}
	c.unqueuedBuffers = nil

	q, bs, err := newAudioQueue(c.sampleRate, c.channelCount, c.oneBufferSizeInBytes)
	if err != nil {
		return fmt.Errorf("oto: rebuilding AudioQueue failed: %w", err)
	}
	c.audioQueue = q
	c.unqueuedBuffers = bs
	c.enqueuedFrames = 0
	c.timelineOrigin = 0
	return nil
}

func (c *context) Err() error {
	return c.err.Load()
}

func render(inUserData unsafe.Pointer, inAQ _AudioQueueRef, inBuffer _AudioQueueBufferRef) {
	theContext.cond.L.Lock()
	defer theContext.cond.L.Unlock()
	// Drop callbacks from a previously-disposed queue: after rebuildAudioQueue,
	// late-delivered callbacks for the old queue would otherwise inject stale
	// buffer pointers into c.unqueuedBuffers.
	if inAQ != theContext.audioQueue {
		return
	}
	theContext.unqueuedBuffers = append(theContext.unqueuedBuffers, inBuffer)
	theContext.cond.Signal()
}

func startRetryDelay(count int) time.Duration {
	switch {
	case count == 0:
		return 10 * time.Millisecond
	case count == 1:
		return 20 * time.Millisecond
	case count == 2:
		return 50 * time.Millisecond
	case count < 10:
		return 100 * time.Millisecond
	default:
		return time.Second
	}
}
