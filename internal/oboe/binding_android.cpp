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
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <memory>
#include <mutex>
#include <thread>
#include <vector>

namespace {

class Player;

// Status is the outcome of an operation. msg is null on success, and describes
// the failure otherwise. retryable is false for a configuration error, which
// would fail the same way however many times it is tried.
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
  // pass, and that LoopStartRetry attempts it again.
  kStartDeferred,

  // kRunning means that the stream was started and has not been paused, closed
  // or disconnected since.
  kRunning,
};

// AudioApiForSdk returns the API to play with.
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
  // in one process. It is because multiple streams can be problematic in both
  // AAudio and OpenSL (#1656, #1660).
  static Stream &GetInstance();

  const char *Play(int sample_rate, int channel_num, int buffer_size_in_bytes);
  const char *Pause();
  const char *Resume();
  const char *Close();
  const char *AppendBuffer(float *buf, size_t len);

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
  void PrepareBuffersLocked();
  Status EnsureStreamLocked();
  Status StartLocked();
  Status StartOrDeferLocked();
  void DeferStartLocked();

  int sample_rate_ = 0;
  int channel_num_ = 0;
  int buffer_size_in_bytes_ = 0;

  // mutex_ guards the stream and the state that follows it, down to
  // deferred_until_. onAudioReady never takes it: onAudioReady is a real-time
  // callback and must not block, and it touches none of them.
  std::mutex mutex_;

  // cond_ wakes start_retry_thread_ when a start is deferred.
  std::condition_variable cond_;

  std::shared_ptr<oboe::AudioStream> stream_;
  State state_ = State::kStopped;
  bool suspended_ = false;
  bool play_called_ = false;

  // start_retries_ is the number of consecutive start attempts that failed for
  // a reason that can pass, and deferred_until_ is when the next one may
  // happen. Both are meaningful only while state_ is kStartDeferred.
  int start_retries_ = 0;
  std::chrono::steady_clock::time_point deferred_until_;

  // fifo_ hands samples from read_thread_ to onAudioReady. It is lock-free, so
  // that onAudioReady never blocks on read_thread_.
  //
  // read_frames_ is the number of frames read from Go at once, and tmp_ is the
  // buffer for one such read.
  //
  // These are sized from the first stream and are never resized, so that
  // onAudioReady and LoopRead can read them without locking. A stream opened again
  // after a disconnection reuses them, and fifo_ absorbs a device whose burst
  // size differs.
  //
  // All the member variables other than the threads must be initialized before
  // read_thread_.
  std::unique_ptr<oboe::FifoBuffer> fifo_;
  std::vector<float> tmp_;
  int read_frames_ = 0;
  std::chrono::microseconds min_wait_{0};

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
      ->setAudioApi(AudioApiForSdk())
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
  if (!stream_) {
    return;
  }
  stream_->close();
  stream_.reset();
}

// PrepareBuffersLocked creates the buffers and the read thread that fills them
// from the stream that is open.
void Stream::PrepareBuffersLocked() {
  int num_frames = stream_->getBufferSizeInFrames();
  // The multiplier is an empirical margin for low-end devices
  // (hajimehoshi/ebiten@4276e296).
  read_frames_ = num_frames * 3;
  tmp_.resize(read_frames_ * channel_num_);
  // The fifo frees space only when onAudioReady runs, so waiting for less than
  // one callback cannot make more space available.
  min_wait_ = std::chrono::microseconds(std::max<int64_t>(
      static_cast<int64_t>(num_frames) * 1000000 / sample_rate_, 1000));
  // The capacity leaves room for one whole read on top of one whole read that
  // is still queued.
  fifo_ = std::make_unique<oboe::FifoBuffer>(channel_num_ * sizeof(float),
                                             read_frames_ * 2);
  read_thread_ = std::make_unique<std::thread>([this]() { LoopRead(); });
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
    return Status{};
  }
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
  // What if the buffer size is not enough?
  if (oboe::Result result = stream_->start(); result != oboe::Result::OK) {
    // A stream disconnected while paused reports it here: no callback runs for
    // a paused stream, so a device that goes away in the background is noticed
    // only at start.
    CloseLocked();
    return StatusFromResult(result);
  }
  return Status{};
}

// StartOrDeferLocked starts playing, and hands the next attempt to
// LoopStartRetry when it fails for a reason that can pass. The returned status
// is set only for a failure that would happen again however long it is retried.
Status Stream::StartOrDeferLocked() {
  Status status = StartLocked();
  if (status.msg && status.retryable) {
    DeferStartLocked();
    return Status{};
  }
  state_ = status.msg ? State::kStopped : State::kRunning;
  start_retries_ = 0;
  return status;
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

  Status restarted;
  {
    std::lock_guard<std::mutex> lock{mutex_};
    if (stream_.get() != oboe_stream) {
      // This error belongs to a stream that was replaced already, e.g. by a
      // Resume that found it disconnected.
      return;
    }
    // Oboe stopped and closed the stream before calling this, so the only way
    // to keep playing is to open a new one.
    stream_.reset();
    state_ = State::kStopped;
    // A stream that goes away while suspended stays closed until Resume, so
    // that a disconnection cannot make a backgrounded app audible.
    if (status.retryable && !suspended_) {
      restarted = StartOrDeferLocked();
    }
  }

  if (status.retryable && !restarted.msg) {
    return;
  }
  // Report what stopped playing, and why it cannot play again when that is
  // known as well.
  oto_oboe_error(const_cast<char *>(status.msg));
  if (restarted.msg) {
    oto_oboe_error(const_cast<char *>(restarted.msg));
  }
}

const char *Stream::Pause() {
  std::lock_guard<std::mutex> lock{mutex_};
  suspended_ = true;
  if (state_ == State::kStartDeferred) {
    // Nothing may start playing while the caller wants silence. Resume attempts
    // it again.
    state_ = State::kStopped;
  }
  if (state_ != State::kRunning) {
    return nullptr;
  }
  state_ = State::kStopped;
  if (oboe::Result result = stream_->pause(); result != oboe::Result::OK) {
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
  fifo_->readNow(audio_data, num_frames);
  return oboe::DataCallbackResult::Continue;
}

Stream::Stream() = default;

void Stream::LoopRead() {
  for (;;) {
    int empty_frames = static_cast<int>(fifo_->getBufferCapacityInFrames() -
                                        fifo_->getFullFramesAvailable());
    if (empty_frames < read_frames_) {
      // Wait for onAudioReady to consume enough frames for one whole read.
      // Sleeping here is fine: only onAudioReady must avoid blocking.
      std::chrono::microseconds wait{
          static_cast<int64_t>(read_frames_ - empty_frames) * 1000000 /
          sample_rate_};
      std::this_thread::sleep_for(std::max(wait, min_wait_));
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

} // extern "C"
