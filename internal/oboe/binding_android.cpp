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
#include <cstdint>
#include <memory>
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
// unknown result is treated as retryable: the number of attempts is bounded,
// so treating a permanent error as temporary costs little, while the opposite
// gives up on a device that would have come back.
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
  void Loop();

  // OpenLocked, StartLocked and ReopenLocked must be called with mutex_ held.
  Status OpenLocked();
  Status StartLocked();
  Status ReopenLocked();

  // Reopen calls ReopenLocked under mutex_.
  Status Reopen();

  int sample_rate_ = 0;
  int channel_num_ = 0;
  int buffer_size_in_bytes_ = 0;

  // mutex_ guards stream_ and suspended_. onAudioReady never takes it:
  // onAudioReady is a real-time callback and must not block, and it touches
  // neither of them.
  std::mutex mutex_;
  std::shared_ptr<oboe::AudioStream> stream_;
  bool suspended_ = false;

  // fifo_ hands samples from the thread to onAudioReady. It is lock-free, so
  // that onAudioReady never blocks on the thread.
  //
  // read_frames_ is the number of frames read from Go at once, and tmp_ is the
  // buffer for one such read.
  //
  // These are sized from the first stream and are never resized, so that
  // onAudioReady and Loop can read them without locking. A stream opened again
  // after a disconnection reuses them, and fifo_ absorbs a device whose burst
  // size differs.
  //
  // All the member variables other than the thread must be initialized before
  // the thread.
  std::unique_ptr<oboe::FifoBuffer> fifo_;
  std::vector<float> tmp_;
  int read_frames_ = 0;
  std::chrono::microseconds min_wait_{0};
  std::unique_ptr<std::thread> thread_;
};

Stream &Stream::GetInstance() {
  static Stream *stream = new Stream();
  return *stream;
}

Status Stream::OpenLocked() {
  if (stream_) {
    return Status{};
  }

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
    return Status{"oboe::SharingMode::Shared is not available", false};
  }
  return Status{};
}

Status Stream::StartLocked() {
  // What if the buffer size is not enough?
  if (oboe::Result result = stream_->start(); result != oboe::Result::OK) {
    return StatusFromResult(result);
  }
  return Status{};
}

const char *Stream::Play(int sample_rate, int channel_num,
                         int buffer_size_in_bytes) {
  sample_rate_ = sample_rate;
  channel_num_ = channel_num;
  buffer_size_in_bytes_ = buffer_size_in_bytes;

  std::lock_guard<std::mutex> lock{mutex_};
  if (Status status = OpenLocked(); status.msg) {
    return status.msg;
  }

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
  thread_ = std::make_unique<std::thread>([this]() { Loop(); });

  return StartLocked().msg;
}

Status Stream::ReopenLocked() {
  stream_.reset();
  if (Status status = OpenLocked(); status.msg) {
    return status;
  }
  // No callback can run between opening and starting, so the fifo can be
  // emptied here. Its contents were mixed for the old device and would
  // otherwise be played late on the new one.
  fifo_->setReadCounter(fifo_->getWriteCounter());
  // A stream opened again while suspended stays paused until Resume, so that a
  // disconnection cannot make a backgrounded app audible.
  if (suspended_) {
    return Status{};
  }
  return StartLocked();
}

Status Stream::Reopen() {
  std::lock_guard<std::mutex> lock{mutex_};
  return ReopenLocked();
}

void Stream::onErrorAfterClose(oboe::AudioStream *oboe_stream,
                               oboe::Result result) {
  // Oboe stopped and closed the stream before calling this, so the only way to
  // keep playing is to open a new one. Oboe calls this on a thread it created
  // for the error, so that needs no thread of its own.
  Status status = StatusFromResult(result);
  constexpr int kTryCount = 5;
  std::chrono::milliseconds interval{20};
  for (int i = 0; i < kTryCount && status.retryable; i++) {
    if (i > 0) {
      // The device might not be ready yet. Wait outside of the lock, and wait
      // longer each time so that a device that takes a while is still reached.
      std::this_thread::sleep_for(interval);
      interval *= 2;
    }
    status = Reopen();
    if (!status.msg) {
      return;
    }
  }
  // Playing is over and nothing else reports this, so hand it to Go.
  oto_oboe_error(const_cast<char *>(status.msg));
}

const char *Stream::Pause() {
  std::lock_guard<std::mutex> lock{mutex_};
  suspended_ = true;
  if (!stream_) {
    return nullptr;
  }
  if (oboe::Result result = stream_->pause(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  return nullptr;
}

const char *Stream::Resume() {
  std::lock_guard<std::mutex> lock{mutex_};
  suspended_ = false;
  if (!fifo_) {
    return "Play is not called yet at Resume";
  }
  if (!stream_) {
    // A disconnection that could not be recovered leaves no stream. Coming back
    // to the foreground is a good moment to reach a device again, and it gives
    // the caller the reason when that still fails.
    return ReopenLocked().msg;
  }
  return StartLocked().msg;
}

const char *Stream::Close() {
  // Nobody calls this so far.
  std::lock_guard<std::mutex> lock{mutex_};
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
  // silence when the thread has not kept up.
  // https://google.github.io/oboe/reference/classoboe_1_1_audio_stream_data_callback.html#ad8a3a9f609df5fd3a5d885cbe1b2204d
  fifo_->readNow(audio_data, num_frames);
  return oboe::DataCallbackResult::Continue;
}

Stream::Stream() = default;

void Stream::Loop() {
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
