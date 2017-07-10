#include <alsa/asoundlib.h>

void check(int *err, int newErr) {
        if (*err < 0) return;
        *err = newErr;
}

int ALSA_hw_params(
		snd_pcm_t         *pcm,
		unsigned          sampleRate,
		unsigned          numChans,
		snd_pcm_format_t  format,
		snd_pcm_uframes_t *buffer_size,
		snd_pcm_uframes_t *period_size
) {
	snd_pcm_hw_params_t *params = NULL;

	int err = 0;
	snd_pcm_hw_params_alloca(&params);
	check(&err, snd_pcm_hw_params_any(pcm, params));

	check(&err, snd_pcm_hw_params_set_access(pcm, params, SND_PCM_ACCESS_RW_INTERLEAVED));
	check(&err, snd_pcm_hw_params_set_format(pcm, params, format));
	check(&err, snd_pcm_hw_params_set_channels(pcm, params, numChans));
	check(&err, snd_pcm_hw_params_set_rate_resample(pcm, params, 1));
	check(&err, snd_pcm_hw_params_set_rate_near(pcm, params, &sampleRate, NULL));
	check(&err, snd_pcm_hw_params_set_buffer_size_near(pcm, params, buffer_size));
	check(&err, snd_pcm_hw_params_set_period_size_near(pcm, params, period_size, NULL));

	check(&err, snd_pcm_hw_params(pcm, params));

	return err;
}