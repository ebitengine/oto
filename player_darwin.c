//
// Created by Christopher Cooper on 7/12/17.
//

#include <sys/param.h>
#include "player_darwin.h"

#pragma mark - utility functions

void MakeBufferSilent (AudioBufferList * ioData) {

  for(UInt32 i=0; i<ioData->mNumberBuffers;i++) {
    memset(ioData->mBuffers[i].mData, 0, ioData->mBuffers[i].mDataByteSize);
  }

}

// the following two functions are here because Go can't work with variable length arrays - it always sees them as an array
// with a single element

void RenderFloatBufferData(UInt32 bufferIndex, UInt32 frame, Float32 f, AudioBufferList *ioData) {

  Float32 *data = (Float32*)ioData->mBuffers[bufferIndex].mData;
  data[frame] = f;
  //data[frame +1] = f;

}

void RenderUint16Data(UInt32 bufferIndex, UInt32 frame, UInt16 u, AudioBufferList *ioData) {
  UInt16 *data = (UInt16*)ioData->mBuffers[bufferIndex].mData;
  data[frame] = u;
}

inline int32_t min(int32_t a, int32_t b) {
  return MIN(a, b);
}


#pragma mark - callback function -

OSStatus GoInputCallback(void *inRefCon,
                    AudioUnitRenderActionFlags *ioActionFlags,
                    const AudioTimeStamp *inTimeStamp,
                    UInt32 inBusNumber,
                    UInt32 inNumberFrames,
                    AudioBufferList *ioData) {

  AudioPlayer *player = (AudioPlayer *) inRefCon;
  ioData->mBuffers[0].mNumberChannels = 2;

  return go_input_callback(player, ioActionFlags, (AudioTimeStamp *) inTimeStamp, inBusNumber, inNumberFrames,
                                   ioData);
}

#pragma mark - output unit and audio render connections

OSStatus CreateAndConnectOutputUnit(AudioPlayer *player, AURenderCallback callback) {

//  10.6 and later: generate description that will match out output device (speakers)
  AudioComponentDescription outputcd = {0}; // 10.6 version
  outputcd.componentType = kAudioUnitType_Output;
  outputcd.componentSubType = kAudioUnitSubType_DefaultOutput;
  outputcd.componentManufacturer = kAudioUnitManufacturer_Apple;

  AudioComponent comp = AudioComponentFindNext(NULL, &outputcd);
  if (comp == NULL) {
    printf("can't get output unit");
    exit(-1);
  }
  OSStatus err = AudioComponentInstanceNew(comp, &player->outputUnit);
  if (err != noErr) {
    return err;
  }
// register render callback
  AURenderCallbackStruct input;
  input.inputProc = callback;
  input.inputProcRefCon = player;
  err = AudioUnitSetProperty(player->outputUnit,
                                  kAudioUnitProperty_SetRenderCallback,
                                  kAudioUnitScope_Input,
                                  0,
                                  &input,
                                  sizeof(input));
  if (err != noErr) {
    return err;
  }

  // Set stream description
  // TODO(cmc) - verify that these are the correct stream parameters. It looks like it expects interleaved data by default,
  // but I'm still unsure about that.
  UInt32 size = sizeof(AudioStreamBasicDescription);
  err = AudioUnitSetProperty(player->outputUnit,
                                  kAudioUnitProperty_StreamFormat,
                                  kAudioUnitScope_Output,
                                  1,
                                  &player->streamFormat,
                                  size);
  if (err != noErr) {
    return err;
  }


  // initialize unit
  return AudioUnitInitialize(player->outputUnit);
}


#pragma mark audio player functions

AudioPlayer NewAudioPlayer(Float64 sampleRate, UInt32 channelsPerFrame, UInt32 bitsPerChannel) {
  AudioStreamBasicDescription description = {0};
  description.mSampleRate = sampleRate;
  description.mChannelsPerFrame = channelsPerFrame;
  description.mBitsPerChannel = bitsPerChannel;
  description.mFormatID = kAudioFormatLinearPCM;
  description.mFormatFlags = kAudioFormatFlagIsPacked;

  AudioPlayer player = {0};
  player.streamFormat = description;

  // set up unit and callback
  CreateAndConnectOutputUnit(&player, GoInputCallback);

  return player;
}

OSStatus StartPlayback(AudioPlayer *player) {
// start playing
  return AudioOutputUnitStart(player->outputUnit);

}

OSStatus StopPlayback(AudioPlayer *player) {
  return AudioOutputUnitStop(player->outputUnit);
}

OSStatus ClosePlayer(AudioPlayer * player) {
  OSStatus err = AudioUnitUninitialize(player->outputUnit);
  if (err != noErr) {
    return err;
  }
  return AudioComponentInstanceDispose(player->outputUnit);
}