#include <AudioToolbox/AudioToolbox.h>

//--------------------------------------------
// AudioPlayer Struct and Associated Functions
//--------------------------------------------

/*!
 * AudioPlayer
 * @field inputUnit - The unit to use to input Audio to the buffer
 * @field outputUnit - The unit to use to output Audio to the HAL
 */
typedef struct AudioPlayer {

    AudioStreamBasicDescription streamFormat;

    AudioUnit outputUnit;

    Float64 sampleRate;

    Float64 firstInputSampleTime;
    Float64 firstOutputSampleTime;
    Float64 inToOutSampleTimeOffset;
    int startingFrameCount;

} AudioPlayer;

/*
 *     Float64             mSampleRate;
    AudioFormatID       mFormatID;
    AudioFormatFlags    mFormatFlags;
    UInt32              mBytesPerPacket;
    UInt32              mFramesPerPacket;
    UInt32              mBytesPerFrame;
    UInt32              mChannelsPerFrame;
    UInt32              mBitsPerChannel;
 */

/*!
 * NewAudioPlayer returns a NewAudioPlayer that is fully initialized
 * @return *AudioPlayer - the audio player
 */
AudioPlayer NewAudioPlayer(Float64 sampleRate, UInt32 channelsPerFrame, UInt32 bitsPerChannel);

/*!
 * StartPlayback initializes and starts playback
 *
 * !!! This is a non blocking function !!!
 *
 * @param player - the audio player to use
 * @return OSStatus - an error code to be checked
 */
OSStatus StartPlayback(AudioPlayer *player);

/*!
 * Stops playback and frees AudioUnit resources
 * @param player - the audio player to stop
 * @return OSStatus - an error code to be checked
 */
OSStatus StopPlayback(AudioPlayer *player);

/*!
 *
 * @param player - the audio player to close. closes access to the underlying device
 * @return OSStatus - an error code to be checked
 */
OSStatus ClosePlayer(AudioPlayer *player);

/*!
 * go_input_callback Is implemented in Go and is called by the GoInputCallback function, which is exported for clarity
 *
 * @param player - a * to an AudioPlayer instead of a void*, which go struggles with
 * @param ioActionFlags - audio render flags
 * @param inTimeStamp - details on the current representation of the current sample frame, machine time, world time, etc
 * @param inBusNumber - the bus number this buffer will play to
 * @param inNumberFrames - the number of frames to place in ioData
 * @param ioData - a pointer to data that needs to be written to
 * @return OSStatus - an error code to be checked
 */
extern OSStatus go_input_callback(AudioPlayer *player,
        AudioUnitRenderActionFlags *ioActionFlags,
        AudioTimeStamp *inTimeStamp,
        UInt32 inBusNumber,
        UInt32 inNumberFrames,
        AudioBufferList *ioData);

/*!
 * GoInputCallback is the callback that implements the required structure of an AudioRenderProc. Specifically, it accepts a void
 * pointer, where as go_callback does not - GoCallback accepts *inRefCon and casts it to *AudioPlayer so that go_callback
 * can handle this data without issue
 *
 * @param inRefCon - a void pointer to AudioPlayer
 * @param ioActionFlags - audio render flags
 * @param inTimeStamp - details on the current representation of the current sample frame, machine time, world time, etc
 * @param inBusNumber - the bus number this buffer will play to
 * @param inNumberFrames - the number of frames to place in ioData
 * @param ioData - a pointer to data that needs to be written to
 * @return OSStatus - an error code to be checked
 */
OSStatus GoInputCallback(void *inRefCon,
                         AudioUnitRenderActionFlags *ioActionFlags,
                         const AudioTimeStamp *inTimeStamp,
                         UInt32 inBusNumber,
                         UInt32 inNumberFrames,
                         AudioBufferList *ioData);


OSStatus OutputCallback(void *inRefCon,
                        AudioUnitRenderActionFlags *ioActionFlags,
                        const AudioTimeStamp *inTimeStamp,
                        UInt32 inBusNumber,
                        UInt32 inNumberFrames,
                        AudioBufferList *ioData);

/*!
 * MakeBufferSilent makes the current buffer silent.
 *
 * @param ioData - the data to be zeroed
 */
void MakeBufferSilent (AudioBufferList *ioData);

/*!
 * RenderBufferData renders to each channel one frame at a time
 * @param bufferIndex - the current index
 * @param frame - the current frame
 * @param f - the value to write to each channel
 * @param ioData - the data to write to
 */
void RenderFloatBufferData(UInt32 bufferIndex, UInt32 frame, Float32 f, AudioBufferList *ioData);
void RenderUint16Data(UInt32 bufferIndex, UInt32 frame, UInt16 u, AudioBufferList *ioData);

/*!
 * MemCpyBuffer copies numBytes to the target from *buffer
 * @param buffer - the buffer containing data to copy
 * @param target - the target buffer to copy to
 * @param numBytes - the number of bytes to copy
 */
void MemCpyBuffer(void *buffer, AudioBufferList *target, UInt32 numBytes);