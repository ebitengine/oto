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

#include "binding_android.h"

#include "_cgo_export.h"
#include "oboe_oboe_Oboe_android.h"

#include <android/api-level.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <condition_variable>
#include <cstdint>
#include <memory>
#include <mutex>
#include <thread>
#include <vector>

namespace {

// Status is the outcome of an operation. msg is null on success, and describes
// the failure otherwise. retryable, which is meaningful only for a failure, is
// false for a configuration error, which would fail the same way however many
// times it is tried.
struct Status {
  const char *msg = nullptr;
  bool retryable = false;
};

// Retryable reports whether opening a stream again might succeed later. An
// unknown result is treated as retryable: retrying one that never succeeds
// costs one attempt a second, while giving up on a device that would have come
// back leaves the process silent for good.
bool Retryable(oboe::Result result) {
  switch (result) {
  case oboe::Result::ErrorIllegalArgument:
  case oboe::Result::ErrorInvalidFormat:
  case oboe::Result::ErrorInvalidHandle:
  case oboe::Result::ErrorInvalidRate:
  case oboe::Result::ErrorNull:
  case oboe::Result::ErrorOutOfRange:
  case oboe::Result::ErrorUnimplemented:
    return false;
  default:
    return true;
  }
}

Status StatusFromResult(oboe::Result result) {
  return Status{oboe::convertToText(result), Retryable(result)};
}

// kStableRunDuration is how long a stream must keep playing for its start to
// count as a success. A stream that goes away sooner managed to start but not
// to keep playing, so the delay before the next attempt keeps growing.
constexpr std::chrono::seconds kStableRunDuration{3};

// StartRetryDelay returns how long to wait before the next start attempt after
// count consecutive failures. The first delays are short, for a device that is
// about to be ready, and they level off at one second, for a condition that can
// last as long as the user leaves it.
std::chrono::milliseconds StartRetryDelay(int count) {
  if (count == 0) {
    return std::chrono::milliseconds{10};
  }
  if (count == 1) {
    return std::chrono::milliseconds{20};
  }
  if (count == 2) {
    return std::chrono::milliseconds{50};
  }
  if (count < 10) {
    return std::chrono::milliseconds{100};
  }
  return std::chrono::milliseconds{1000};
}

// State is the actual state of the stream. It is independent of suspended_, the
// state requested by the caller.
enum class State {
  // kStopped means that nothing is playing and a start attempt can happen at
  // any time.
  kStopped,

  // kStartDeferred means that a start attempt failed for a reason that can
  // be temporary, and that LoopStartRetry attempts it again.
  kStartDeferred,

  // kRunning means that the stream was started and has not been paused, closed
  // or disconnected since.
  kRunning,
};

// AudioApiForSdk returns the API to play with on the running version of
// Android.
oboe::AudioApi AudioApiForSdk() {
  // AAudio binds a stream to one device and disconnects it when the routing
  // changes, which onErrorAfterClose recovers from. Before Android R the
  // disconnection was not always reported (google/oboe#893), leaving no way to
  // notice, so OpenSL ES, which follows the routing on its own, is used there.
  if (oboe::getSdkVersion() < __ANDROID_API_R__) {
    return oboe::AudioApi::OpenSLES;
  }
  return oboe::AudioApi::Unspecified;
}

class Stream : public oboe::AudioStreamDataCallback,
               public oboe::AudioStreamErrorCallback {
public:
  // GetInstance returns the instance of Stream. Only one Stream object is used
  // in one process, because multiple streams can be problematic in both AAudio
  // and OpenSL (#1656, #1660).
  static Stream &GetInstance();

  const char *Play(int sample_rate, int channel_num, int buffer_size_in_bytes);
  const char *Pause();
  const char *Resume();
  const char *Close();
  const char *AppendBuffer(float *buf, size_t len);
  int64_t Delay();

  oboe::DataCallbackResult onAudioReady(oboe::AudioStream *oboe_stream,
                                        void *audio_data,
                                        int32_t num_frames) override;
  void onErrorAfterClose(oboe::AudioStream *oboe_stream,
                         oboe::Result result) override;

private:
  Stream();
  void LoopRead();
  void LoopStartRetry();

  // The Locked functions must be called with mutex_ held.
  Status OpenLocked();
  void CloseLocked();
  void ForgetDelayLocked();
  void MeasureDelay();
  void PrepareBuffersLocked();
  void ConfigureRefillLocked();
  Status EnsureStreamLocked();
  Status StartLocked();
  Status StartOrDeferLocked();
  void DeferStartLocked();

  int sample_rate_ = 0;
  int channel_num_ = 0;
  int buffer_size_in_bytes_ = 0;

  // mutex_ guards the stream and the state that follows it, down to
  // running_since_. onAudioReady never takes it: onAudioReady is a real-time
  // callback and must not block, and it touches none of them.
  std::mutex mutex_;

  // cond_ wakes start_retry_thread_ when a start is deferred.
  std::condition_variable cond_;

  std::shared_ptr<oboe::AudioStream> stream_;

  // audio_api_ is the API stream_ is opened with. It falls back to OpenSL ES
  // for good once AAudio refuses to play.
  oboe::AudioApi audio_api_ = AudioApiForSdk();

  State state_ = State::kStopped;
  bool suspended_ = false;
  bool play_called_ = false;

  // start_retries_ is the number of start attempts since the last one that
  // played for kStableRunDuration, and deferred_until_ is when the next one
  // may happen. deferred_until_ is meaningful only while state_ is
  // kStartDeferred.
  int start_retries_ = 0;
  std::chrono::steady_clock::time_point deferred_until_;

  // running_since_ is when the current run started. It is meaningful only
  // while state_ is kRunning.
  std::chrono::steady_clock::time_point running_since_;

  // fifo_ hands samples from read_thread_ to onAudioReady. It is lock-free, so
  // that onAudioReady never blocks on read_thread_.
  //
  // read_frames_ is the number of frames read from Go at once, and tmp_ is the
  // buffer for one such read.
  //
  // These are made once and are never resized, so that onAudioReady and
  // LoopRead can read them without locking. A stream opened again after a
  // disconnection reuses them. A read is 10 ms at the context's sample rate,
  // and fifo_ holds one second, more than any device's queue. How much of it
  // LoopRead fills follows the stream playing: fill_target_frames_ and
  // min_wait_us_ below.
  //
  // All the member variables other than the threads must be initialized before
  // read_thread_.
  std::unique_ptr<oboe::FifoBuffer> fifo_;
  std::vector<float> tmp_;
  int read_frames_ = 0;

  // fill_target_frames_ is how many frames LoopRead keeps queued in fifo_ for
  // the stream playing, and min_wait_us_ the shortest it sleeps between reads,
  // in microseconds. They are set as each stream opens, as devices differ: a
  // Bluetooth headset's bursts can be twenty times a phone speaker's.
  // max_callback_ is the most frames a callback has asked for since. LoopRead
  // keeps two of those and a read queued, for a device whose callbacks outgrow
  // its bursts.
  std::atomic<int> fill_target_frames_{0};
  std::atomic<int64_t> min_wait_us_{1000};
  std::atomic<int> max_callback_{0};

  // The delay. Every kDelayEvery, LoopRead measures when the frame written to
  // fifo_ next will be heard: delay_written_ is the write counter then, and
  // delay_heard_at_ the time, while delay_known_ is set. delay_gen_ counts the
  // streams dropped, paused and opened, so that a measurement of a stream that
  // changed meanwhile is dropped. delay_mutex_ guards these, and is taken after
  // mutex_ where both are.
  std::mutex delay_mutex_;
  bool delay_known_ = false;
  int64_t delay_written_ = 0;
  std::chrono::steady_clock::time_point delay_heard_at_;
  int delay_gen_ = 0;

  // read_thread_ runs LoopRead, which reads from Go into fifo_.
  std::unique_ptr<std::thread> read_thread_;

  // start_retry_thread_ runs LoopStartRetry, which attempts a deferred start
  // until playing starts or ends for good. It is created by the first
  // deferral, so a process that never needs one never pays for it.
  std::unique_ptr<std::thread> start_retry_thread_;
};

Stream &Stream::GetInstance() {
  static Stream *stream = new Stream();
  return *stream;
}

Status Stream::OpenLocked() {
  oboe::AudioStreamBuilder builder;
  builder.setDirection(oboe::Direction::Output)
      ->setAudioApi(audio_api_)
      ->setPerformanceMode(oboe::PerformanceMode::LowLatency)
      ->setSharingMode(oboe::SharingMode::Shared)
      ->setFormat(oboe::AudioFormat::Float)
      ->setChannelCount(channel_num_)
      ->setSampleRate(sample_rate_)
      ->setDataCallback(this)
      ->setErrorCallback(this);
  if (buffer_size_in_bytes_) {
    int buffer_size_in_frames = buffer_size_in_bytes_ / channel_num_ / 4;
    builder.setBufferCapacityInFrames(buffer_size_in_frames);
  }
  oboe::Result result = builder.openStream(stream_);
  if (result != oboe::Result::OK) {
    return StatusFromResult(result);
  }
  if (stream_->getSharingMode() != oboe::SharingMode::Shared) {
    CloseLocked();
    return Status{"oboe::SharingMode::Shared is not available", false};
  }
  return Status{};
}

// CloseLocked closes the current stream, if any, and drops it. Closing stops a
// running stream, and its result is of no interest: the stream is not used
// again either way.
void Stream::CloseLocked() {
  ForgetDelayLocked();
  if (!stream_) {
    return;
  }
  stream_->close();
  stream_.reset();
}

// PrepareBuffersLocked creates the buffers, which depend only on the sample
// rate.
void Stream::PrepareBuffersLocked() {
  // A player hands over only what it has buffered, and the rest of a read is
  // silence, so a read must stay small next to a player's buffer.
  read_frames_ = sample_rate_ / 100;
  tmp_.resize(read_frames_ * channel_num_);
  // One second of sound is more than any device's queue.
  fifo_ = std::make_unique<oboe::FifoBuffer>(channel_num_ * sizeof(float),
                                             sample_rate_);
}

// ConfigureRefillLocked sets how LoopRead refills fifo_ for the stream just
// opened: how much it keeps queued and how long it sleeps at least between
// reads, and it forgets the largest callback seen so far.
void Stream::ConfigureRefillLocked() {
  int burst = stream_->getFramesPerBurst();
  // Before each read, at least two bursts stay queued, so that a whole burst
  // remains after any callback, and at least 25 ms: about three 384-frame
  // OpenSL ES buffers at 48 kHz, the queue that stopped occasional noises on a
  // low-end device where two were not enough (hajimehoshi/ebiten@4276e296).
  int low = std::max(sample_rate_ / 40, 2 * burst);
  // The fill target is that and one read on top.
  fill_target_frames_.store(low + read_frames_);
  max_callback_.store(0);
  // Half a burst, so the queue is topped up well before the next callback.
  min_wait_us_.store(std::max<int64_t>(
      static_cast<int64_t>(burst) * 1000000 / sample_rate_ / 2, 1000));
}

// EnsureStreamLocked opens a stream unless there already is one.
Status Stream::EnsureStreamLocked() {
  if (stream_) {
    return Status{};
  }
  if (Status status = OpenLocked(); status.msg) {
    return status;
  }
  if (!fifo_) {
    PrepareBuffersLocked();
    ConfigureRefillLocked();
    // The read thread starts once all it reads is set.
    read_thread_ = std::make_unique<std::thread>([this]() { LoopRead(); });
    return Status{};
  }
  ConfigureRefillLocked();
  // No callback can run before the stream is started, so the fifo can be
  // emptied here. Its contents were mixed for the device that went away and
  // would otherwise be played late on the new one.
  fifo_->setReadCounter(fifo_->getWriteCounter());
  return Status{};
}

// StartLocked opens a stream if the last one is gone, and starts playing. It
// leaves no stream behind when it fails, so that the next attempt starts from
// a new one.
Status Stream::StartLocked() {
  if (Status status = EnsureStreamLocked(); status.msg) {
    return status;
  }
  // The zero timeout requests the start without waiting for the stream to
  // reach the started state. mutex_ is held here, and Pause and Resume take it
  // on the caller's UI thread, so nothing under it may wait on a device. A
  // stream that never starts reports it through the error callback.
  if (oboe::Result result = stream_->start(0); result != oboe::Result::OK) {
    // A stream disconnected while paused reports it here: no callback runs for
    // a paused stream, so a device that goes away in the background is noticed
    // only at start.
    CloseLocked();
    return StatusFromResult(result);
  }
  return Status{};
}

// StartOrDeferLocked starts playing. A failure that can pass hands the next
// attempt to LoopStartRetry, and a failure that cannot falls back to OpenSL ES.
// The returned status is set only for a failure that no further attempt would
// recover from.
Status Stream::StartOrDeferLocked() {
  Status status = StartLocked();
  if (status.msg && !status.retryable &&
      audio_api_ != oboe::AudioApi::OpenSLES) {
    // Some devices refuse an AAudio stream for a configuration that plays
    // everywhere else (google/oboe#1293). OpenSL ES, which followed the same
    // configuration on every device before AAudio, is tried before playing is
    // given up on, and takes over for the rest of the process.
    audio_api_ = oboe::AudioApi::OpenSLES;
    status = StartLocked();
  }
  if (status.msg && status.retryable) {
    DeferStartLocked();
    return Status{};
  }
  if (status.msg) {
    state_ = State::kStopped;
    return status;
  }
  state_ = State::kRunning;
  running_since_ = std::chrono::steady_clock::now();
  return Status{};
}

// DeferStartLocked schedules the next start attempt.
void Stream::DeferStartLocked() {
  state_ = State::kStartDeferred;
  deferred_until_ =
      std::chrono::steady_clock::now() + StartRetryDelay(start_retries_);
  start_retries_++;
  if (!start_retry_thread_) {
    start_retry_thread_ = std::make_unique<std::thread>([this]() { LoopStartRetry(); });
    return;
  }
  cond_.notify_one();
}

const char *Stream::Play(int sample_rate, int channel_num,
                         int buffer_size_in_bytes) {
  std::lock_guard<std::mutex> lock{mutex_};
  sample_rate_ = sample_rate;
  channel_num_ = channel_num;
  buffer_size_in_bytes_ = buffer_size_in_bytes;
  play_called_ = true;

  // A device can be busy while this process is starting, e.g. when another app
  // is still holding it. Playing then starts as soon as it is free, and the
  // caller gets a context that is silent until then.
  return StartOrDeferLocked().msg;
}

void Stream::onErrorAfterClose(oboe::AudioStream *oboe_stream,
                               oboe::Result result) {
  // Oboe calls this on a thread it created for the error, so that needs no
  // thread of its own.
  Status status = StatusFromResult(result);

  {
    std::lock_guard<std::mutex> lock{mutex_};
    if (stream_.get() != oboe_stream) {
      // This error belongs to a stream that was replaced already, e.g. by a
      // Resume that found it disconnected.
      return;
    }
    // A run that lasted long enough is a success, so that a device that plays
    // and goes away once is reached again at the shortest delay.
    if (state_ == State::kRunning &&
        std::chrono::steady_clock::now() - running_since_ >=
            kStableRunDuration) {
      start_retries_ = 0;
    }
    // Oboe stopped and closed the stream before calling this, so the only way
    // to keep playing is to open a new one.
    ForgetDelayLocked();
    stream_.reset();
    state_ = State::kStopped;
    // A stream that goes away while suspended stays closed until Resume, so
    // that a disconnection cannot make a backgrounded app audible.
    if (status.retryable && !suspended_) {
      // Opening again waits for the same delay as a start that failed. A
      // stream that keeps going away as soon as it starts would otherwise be
      // opened again as fast as the device can lose it.
      DeferStartLocked();
      return;
    }
  }

  if (!status.msg || status.retryable) {
    return;
  }
  // Report what stopped playing.
  oto_oboe_error(const_cast<char *>(status.msg));
}

const char *Stream::Pause() {
  std::lock_guard<std::mutex> lock{mutex_};
  suspended_ = true;
  ForgetDelayLocked();
  if (state_ == State::kStartDeferred) {
    // Nothing may start playing while the caller wants silence. Resume attempts
    // it again.
    state_ = State::kStopped;
  }
  if (state_ != State::kRunning) {
    return nullptr;
  }
  state_ = State::kStopped;
  // The zero timeout requests the pause without waiting for the stream to
  // reach the paused state. This runs on the caller's UI thread, where waiting
  // on a device is what an ANR is made of, and the request alone is what stops
  // the samples from being consumed.
  if (oboe::Result result = stream_->pause(0); result != oboe::Result::OK) {
    // Pausing fails on a stream that was disconnected, which cannot play
    // either. Closing it keeps the process silent, and Resume opens another
    // one.
    CloseLocked();
  }
  return nullptr;
}

const char *Stream::Resume() {
  std::lock_guard<std::mutex> lock{mutex_};
  suspended_ = false;
  if (!play_called_) {
    return "Play is not called yet at Resume";
  }
  if (state_ == State::kRunning) {
    return nullptr;
  }
  // Coming back to the foreground is a good moment to reach a device again, so
  // a pending backoff is dropped and the attempt happens now.
  start_retries_ = 0;
  return StartOrDeferLocked().msg;
}

const char *Stream::Close() {
  // Nobody calls this so far.
  std::lock_guard<std::mutex> lock{mutex_};
  state_ = State::kStopped;
  if (!stream_) {
    return nullptr;
  }
  if (oboe::Result result = stream_->stop(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  if (oboe::Result result = stream_->close(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  ForgetDelayLocked();
  stream_.reset();
  return nullptr;
}

oboe::DataCallbackResult Stream::onAudioReady(oboe::AudioStream *oboe_stream,
                                              void *audio_data,
                                              int32_t num_frames) {
  // This runs on a real-time thread, where locking, allocating or blocking can
  // glitch the audio or time the stream out. readNow fills the remainder with
  // silence when the read thread has not kept up.
  // https://google.github.io/oboe/reference/classoboe_1_1_audio_stream_data_callback.html#ad8a3a9f609df5fd3a5d885cbe1b2204d
  if (num_frames > max_callback_.load(std::memory_order_relaxed)) {
    max_callback_.store(num_frames, std::memory_order_relaxed);
  }
  fifo_->readNow(audio_data, num_frames);
  return oboe::DataCallbackResult::Continue;
}

Stream::Stream() = default;

// kDelayEvery is how often LoopRead measures the delay.
constexpr std::chrono::milliseconds kDelayEvery{100};

// ForgetDelayLocked forgets the delay measured, as the stream it was measured
// on is dropped, paused or replaced.
void Stream::ForgetDelayLocked() {
  std::lock_guard<std::mutex> lock{delay_mutex_};
  delay_gen_++;
  delay_known_ = false;
}

// MeasureDelay measures when the frame written to fifo_ next will be heard.
void Stream::MeasureDelay() {
  std::shared_ptr<oboe::AudioStream> stream;
  int gen;
  {
    // Pause and Resume hold mutex_ only briefly; a measurement is skipped
    // rather than waited for.
    std::unique_lock<std::mutex> lock{mutex_, std::try_to_lock};
    if (!lock.owns_lock() || !stream_ || state_ != State::kRunning) {
      return;
    }
    stream = stream_;
    std::lock_guard<std::mutex> delay_lock{delay_mutex_};
    gen = delay_gen_;
  }
  // The stream is asked outside mutex_, as asking it can wait on the device.
  // Only this thread writes to fifo_, and a callback reads from it as the
  // stream takes frames, so a measurement is kept only if no callback ran
  // meanwhile.
  for (int i = 0; i < 3; i++) {
    int64_t read = fifo_->getReadCounter();
    double ms;
    if (auto result = stream->calculateLatencyMillis(); result) {
      // The next frame the stream takes is heard this long from now. It can
      // come out negative, as around an underrun.
      ms = std::max(0.0, result.value());
    } else if (result.error() == oboe::Result::ErrorUnimplemented) {
      // OpenSL ES has no timestamps: the frames the stream holds are the
      // estimate.
      ms = stream->getBufferSizeInFrames() * 1000.0 / sample_rate_;
    } else {
      return;
    }
    auto now = std::chrono::steady_clock::now();
    if (fifo_->getReadCounter() != read) {
      continue;
    }
    int64_t written = fifo_->getWriteCounter();
    // The frames queued in fifo_ are heard before the frame written next.
    auto heard_at =
        now + std::chrono::microseconds(std::llround(
                  ms * 1000 + (written - read) * 1e6 / sample_rate_));
    std::lock_guard<std::mutex> lock{delay_mutex_};
    if (delay_gen_ != gen) {
      return;
    }
    delay_known_ = true;
    delay_written_ = written;
    delay_heard_at_ = heard_at;
    return;
  }
}

// Delay returns how many of the frames read from Go are not heard yet, or -1
// while the stream has not been measured.
int64_t Stream::Delay() {
  std::lock_guard<std::mutex> lock{delay_mutex_};
  if (!delay_known_) {
    return -1;
  }
  // The frame written next at the measurement is heard at delay_heard_at_, and
  // the frames written since after it.
  std::chrono::duration<double> until =
      delay_heard_at_ - std::chrono::steady_clock::now();
  int64_t frames = fifo_->getWriteCounter() - delay_written_ +
                   std::llround(until.count() * sample_rate_);
  return std::max<int64_t>(frames, 0);
}

void Stream::LoopRead() {
  auto measured = std::chrono::steady_clock::now();
  for (;;) {
    if (auto now = std::chrono::steady_clock::now();
        now - measured >= kDelayEvery) {
      measured = now;
      MeasureDelay();
    }
    // The queue is kept at the larger of the target and two of the largest
    // callbacks so far plus a read, for a device whose callbacks outgrow its
    // bursts, and at most the fifo's capacity less one read.
    int target = std::max(fill_target_frames_.load(),
                          2 * max_callback_.load(std::memory_order_relaxed) +
                              read_frames_);
    target = std::min(target,
                      static_cast<int>(fifo_->getBufferCapacityInFrames()) -
                          read_frames_);
    int full_frames = static_cast<int>(fifo_->getFullFramesAvailable());
    if (full_frames + read_frames_ > target) {
      // Wait for onAudioReady to consume enough frames for one whole read.
      // Sleeping here is fine: only onAudioReady must avoid blocking.
      std::chrono::microseconds wait{
          static_cast<int64_t>(full_frames + read_frames_ - target) * 1000000 /
          sample_rate_};
      std::this_thread::sleep_for(
          std::max(wait, std::chrono::microseconds(min_wait_us_.load())));
      continue;
    }
    oto_oboe_read(&tmp_[0], tmp_.size());
    fifo_->write(&tmp_[0], read_frames_);
  }
}

void Stream::LoopStartRetry() {
  for (;;) {
    Status status;
    {
      std::unique_lock<std::mutex> lock{mutex_};
      if (state_ != State::kStartDeferred || suspended_) {
        // Pausing and a start that succeeded both end the deferral.
        // DeferStartLocked wakes this up when the next one begins.
        cond_.wait(lock);
        continue;
      }
      if (std::chrono::steady_clock::now() < deferred_until_) {
        cond_.wait_until(lock, deferred_until_);
        continue;
      }
      status = StartOrDeferLocked();
    }
    if (status.msg) {
      // A start that would fail the same way however long it is retried is the
      // end of playing.
      oto_oboe_error(const_cast<char *>(status.msg));
      return;
    }
  }
}

} // namespace

extern "C" {

const char *oto_oboe_Play(int sample_rate, int channel_num,
                          int buffer_size_in_bytes) {
  return Stream::GetInstance().Play(sample_rate, channel_num,
                                    buffer_size_in_bytes);
}

const char *oto_oboe_Suspend() { return Stream::GetInstance().Pause(); }

const char *oto_oboe_Resume() { return Stream::GetInstance().Resume(); }

int64_t oto_oboe_Delay() { return Stream::GetInstance().Delay(); }

} // extern "C"
