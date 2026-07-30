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

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <memory>
#include <thread>
#include <vector>

namespace {

class Player;

class Stream : public oboe::AudioStreamDataCallback {
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

private:
  Stream();
  void Loop();

  int sample_rate_ = 0;
  int channel_num_ = 0;

  std::shared_ptr<oboe::AudioStream> stream_;

  // fifo_ hands samples from the thread to onAudioReady. It is lock-free, so
  // that onAudioReady never blocks on the thread.
  //
  // read_frames_ is the number of frames read from Go at once, and tmp_ is the
  // buffer for one such read.
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

const char *Stream::Play(int sample_rate, int channel_num,
                         int buffer_size_in_bytes) {
  sample_rate_ = sample_rate;
  channel_num_ = channel_num;

  if (!stream_) {
    oboe::AudioStreamBuilder builder;
    builder.setDirection(oboe::Direction::Output)
        ->setPerformanceMode(oboe::PerformanceMode::LowLatency)
        ->setSharingMode(oboe::SharingMode::Shared)
        ->setFormat(oboe::AudioFormat::Float)
        ->setChannelCount(channel_num_)
        ->setSampleRate(sample_rate_)
        ->setDataCallback(this);
    if (buffer_size_in_bytes) {
      int buffer_size_in_frames = buffer_size_in_bytes / channel_num / 4;
      builder.setBufferCapacityInFrames(buffer_size_in_frames);
    }
    oboe::Result result = builder.openStream(stream_);
    if (result != oboe::Result::OK) {
      return oboe::convertToText(result);
    }
  }
  if (stream_->getSharingMode() != oboe::SharingMode::Shared) {
    return "oboe::SharingMode::Shared is not available";
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

  // What if the buffer size is not enough?
  if (oboe::Result result = stream_->start(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  return nullptr;
}

const char *Stream::Pause() {
  if (!stream_) {
    return nullptr;
  }
  if (oboe::Result result = stream_->pause(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  return nullptr;
}

const char *Stream::Resume() {
  if (!stream_) {
    return "Play is not called yet at Resume";
  }
  if (oboe::Result result = stream_->start(); result != oboe::Result::OK) {
    return oboe::convertToText(result);
  }
  return nullptr;
}

const char *Stream::Close() {
  // Nobody calls this so far.
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
