import { IdentityMatch, MoviePlaybackCheck } from './types';
import { MessageKey, Translate } from './i18n/core';

// What the fingerprint said, in a chip's words: the acoustic fingerprint of
// a recording, the perceptual hash of a picture, the text fingerprint of a
// document or the frame fingerprint of a film. The detail the server
// composed says the rest.
const methodKeys: Record<string, MessageKey> = {
  perceptual_hash: 'identity.imageHash',
  text_fingerprint: 'identity.textFingerprint',
  frame_fingerprint: 'identity.frameFingerprint',
};

const methodNoun = (method: string, t: Translate): string => t(methodKeys[method] ?? 'identity.fingerprint');

export const identityLabel = (identity: IdentityMatch, t: Translate): string => {
  const by = methodNoun(identity.method, t);
  switch (identity.effect) {
    case 'joined': return t('identity.matchedBy', { method: by });
    case 'confirmed': return t('identity.confirmedBy', { method: by });
    case 'kept_apart': return t('identity.separatedBy', { method: by });
  }
  switch (identity.verdict) {
    case 'same':
      return identity.method === 'perceptual_hash'
        ? t('identity.samePicture')
        : identity.method === 'text_fingerprint'
          ? t('identity.sameText')
          : identity.method === 'frame_fingerprint'
            ? t('identity.sameBeginningFrames')
            : t('identity.sameBeginningFingerprint');
    case 'different': return t('identity.differentBy', { method: by });
    default: return identity.method === 'perceptual_hash' ? t('identity.hashed') : t('identity.fingerprinted');
  }
};

// How the sample's fingerprint went, for the recording or movie card: the
// acoustic fingerprint of an audio sample, the frame fingerprint of a
// video sample.
export const fingerprintLabel = (check: MoviePlaybackCheck, t: Translate): string | null => {
  if (check.method !== 'isolated_audio_sample') return frameFingerprintLabel(check, t);
  switch (check.fingerprintStatus) {
    case undefined:
    case '':
      return null;
    case 'computed': {
      const seconds = check.fingerprint?.seconds;
      return seconds
        ? t('identity.acousticTaken', { seconds: seconds.toFixed(0), frames: check.fingerprint?.frames ?? 0 })
        : t('identity.acousticTakenShort');
    }
    case 'pending':
    case 'timeout':
      return t('identity.acousticPending');
    case 'fetch_failed': return t('identity.acousticFetchFailed');
    case 'decode_failed': return t('identity.acousticDecodeFailed');
    case 'too_short': return t('identity.acousticTooShort');
    case 'unsupported': return t('identity.acousticUnsupported');
    case 'invalid': return t('identity.acousticInvalid');
    default: return t('identity.acousticFailed');
  }
};

// How a video sample's frame fingerprint went.
const frameFingerprintLabel = (check: MoviePlaybackCheck, t: Translate): string | null => {
  switch (check.fingerprintStatus) {
    case undefined:
    case '':
      return null;
    case 'computed': {
      const seconds = check.fingerprint?.seconds;
      const frames = Math.floor((check.fingerprint?.frames ?? 0) / 8);
      return seconds
        ? t('identity.frameTaken', { seconds: seconds.toFixed(1), frames })
        : t('identity.frameTakenShort');
    }
    case 'too_short': return t('identity.frameTooShort');
    case 'unsupported': return t('identity.frameUnsupported');
    default: return t('identity.frameFailed');
  }
};
