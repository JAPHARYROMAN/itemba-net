import { IdentityMatch, MoviePlaybackCheck } from './types';

// What the acoustic fingerprint said, in a chip's words. The detail the
// server composed says the rest.
export const identityLabel = (identity: IdentityMatch): string => {
  switch (identity.effect) {
    case 'joined': return 'Matched by fingerprint';
    case 'confirmed': return 'Confirmed by fingerprint';
    case 'kept_apart': return 'Separated by fingerprint';
  }
  switch (identity.verdict) {
    case 'same': return 'Same beginning by fingerprint';
    case 'different': return 'Different by fingerprint';
    default: return 'Fingerprinted';
  }
};

// How the sample's fingerprint went, for the recording card.
export const fingerprintLabel = (check: MoviePlaybackCheck): string | null => {
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
