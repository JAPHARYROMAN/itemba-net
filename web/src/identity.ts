import { IdentityMatch, MoviePlaybackCheck } from './types';

// What the fingerprint said, in a chip's words: the acoustic fingerprint of
// a recording, the perceptual hash of a picture, the text fingerprint of a
// document or the frame fingerprint of a film. The detail the server
// composed says the rest.
const methodNoun = (method: string): string => {
  switch (method) {
    case 'perceptual_hash': return 'image hash';
    case 'text_fingerprint': return 'text fingerprint';
    case 'frame_fingerprint': return 'frame fingerprint';
    default: return 'fingerprint';
  }
};

export const identityLabel = (identity: IdentityMatch): string => {
  const by = methodNoun(identity.method);
  switch (identity.effect) {
    case 'joined': return `Matched by ${by}`;
    case 'confirmed': return `Confirmed by ${by}`;
    case 'kept_apart': return `Separated by ${by}`;
  }
  switch (identity.verdict) {
    case 'same': return identity.method === 'perceptual_hash' ? 'Same picture by hash' : identity.method === 'text_fingerprint' ? 'Same text by fingerprint' : identity.method === 'frame_fingerprint' ? 'Same beginning by frames' : 'Same beginning by fingerprint';
    case 'different': return `Different by ${by}`;
    default: return identity.method === 'perceptual_hash' ? 'Hashed' : 'Fingerprinted';
  }
};

// How the sample's fingerprint went, for the recording or movie card: the
// acoustic fingerprint of an audio sample, the frame fingerprint of a
// video sample.
export const fingerprintLabel = (check: MoviePlaybackCheck): string | null => {
  if (check.method !== 'isolated_audio_sample') return frameFingerprintLabel(check);
  switch (check.fingerprintStatus) {
    case undefined:
    case '':
      return null;
    case 'computed': {
      const seconds = check.fingerprint?.seconds;
      return seconds ? `Acoustic fingerprint taken over ${seconds.toFixed(0)}s of the beginning (${check.fingerprint?.frames ?? 0} frames).` : 'Acoustic fingerprint taken.';
    }
    case 'pending':
    case 'timeout':
      return 'The acoustic fingerprint did not finish within the sample.';
    case 'fetch_failed': return 'No acoustic fingerprint: the file’s beginning could not be fetched for decoding.';
    case 'decode_failed': return 'No acoustic fingerprint: the beginning could not be decoded offline (AAC in MP4 with a trailing index decodes only in the player).';
    case 'too_short': return 'No acoustic fingerprint: too little audio decoded.';
    case 'unsupported': return 'No acoustic fingerprint: the isolated browser cannot decode offline.';
    case 'invalid': return 'No acoustic fingerprint: the page returned a malformed one.';
    default: return 'No acoustic fingerprint: the computation failed.';
  }
};

// How a video sample's frame fingerprint went.
const frameFingerprintLabel = (check: MoviePlaybackCheck): string | null => {
  switch (check.fingerprintStatus) {
    case undefined:
    case '':
      return null;
    case 'computed': {
      const seconds = check.fingerprint?.seconds;
      const frames = Math.floor((check.fingerprint?.frames ?? 0) / 8);
      return seconds ? `Frame fingerprint taken over ${seconds.toFixed(1)}s of the beginning (${frames} frames, half a second apart); the index compares it with every film sampled.` : 'Frame fingerprint taken.';
    }
    case 'too_short': return 'No frame fingerprint: too few frames were captured before the sample ended.';
    case 'unsupported': return 'No frame fingerprint: the isolated browser could not read the decoded frames.';
    default: return 'No frame fingerprint: the capture failed.';
  }
};
