import { ActiveRelayStream, MediaKind } from '../types';

// Media-kind detection, relay addressing and the small formatting helpers the
// players and viewers share. Detection is by URL extension first and by the
// scout's format label second; anything undecided is treated as video, which
// is what the relay player always assumed before kinds existed.

const VIDEO_EXT = new Set(['mp4', 'm4v', 'webm', 'mkv', 'mov', 'ogv', 'ts', 'm3u8', 'mpd']);
const AUDIO_EXT = new Set(['mp3', 'flac', 'ogg', 'oga', 'opus', 'm4a', 'aac', 'wav', 'weba']);
const IMAGE_EXT = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'svg', 'bmp', 'tif', 'tiff']);
const DOCUMENT_EXT = new Set(['pdf', 'epub', 'txt', 'djvu', 'mobi', 'doc', 'docx', 'odt']);

export const extensionOf = (raw: string): string => {
  try {
    const url = new URL(raw, window.location.origin);
    const match = /\.([a-z0-9]{1,5})$/i.exec(url.pathname);
    return match ? match[1].toLowerCase() : '';
  } catch {
    return '';
  }
};

export const isHlsUrl = (raw: string): boolean => extensionOf(raw) === 'm3u8' || /\.m3u8(?:[?#]|$)/i.test(raw);
export const isDashUrl = (raw: string): boolean => extensionOf(raw) === 'mpd';
export const isPdfUrl = (raw: string): boolean => extensionOf(raw) === 'pdf';

export const detectMediaKind = (raw: string, formatHint?: string): MediaKind => {
  const ext = extensionOf(raw);
  if (AUDIO_EXT.has(ext)) return 'audio';
  if (IMAGE_EXT.has(ext)) return 'image';
  if (DOCUMENT_EXT.has(ext)) return 'document';
  if (VIDEO_EXT.has(ext)) return 'video';
  const hint = (formatHint ?? '').toLowerCase();
  if (hint.includes('audio') || hint.includes('mp3') || hint.includes('flac')) return 'audio';
  if (hint.includes('image')) return 'image';
  if (hint.includes('pdf') || hint.includes('document')) return 'document';
  return 'video';
};

export const kindOf = (stream: ActiveRelayStream): MediaKind => stream.kind ?? detectMediaKind(stream.streamUrl, stream.format);

// Every byte of a protected stream goes through the relay, which applies
// Shield to the target, strips cookies and rewrites HLS playlists onto itself.
export const relayUrl = (raw: string): string => `/api/relay/stream?url=${encodeURIComponent(raw)}`;

export const KIND_LABELS: Record<MediaKind, string> = {
  video: 'Video',
  audio: 'Audio',
  image: 'Image',
  document: 'Document',
};

// A pasted address becomes a relay item when its extension names a media
// type the console presents itself; anything else is a page for the sandbox.
export const streamFromPastedUrl = (raw: string): ActiveRelayStream | null => {
  const ext = extensionOf(raw);
  if (!VIDEO_EXT.has(ext) && !AUDIO_EXT.has(ext) && !IMAGE_EXT.has(ext) && !DOCUMENT_EXT.has(ext)) return null;
  const kind = detectMediaKind(raw);
  let name = raw.split('/').pop() ?? raw;
  try { name = decodeURIComponent(name.split('?')[0]) || name; } catch { /* keep the raw name */ }
  let host = '';
  try { host = new URL(raw).hostname; } catch { /* no host */ }
  return {
    title: name,
    streamUrl: raw,
    provider: host ? `Pasted address · ${host}` : 'Pasted address',
    format: ext === 'm3u8' ? 'HLS (.m3u8)' : ext === 'mpd' ? 'DASH (.mpd)' : `${KIND_LABELS[kind]} (.${ext})`,
    useZeroTrustProxy: true,
    kind,
  };
};

export const formatTime = (seconds: number): string => {
  if (!Number.isFinite(seconds) || seconds < 0) return '--:--';
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const mm = h > 0 ? String(m).padStart(2, '0') : String(m);
  return `${h > 0 ? `${h}:` : ''}${mm}:${String(s).padStart(2, '0')}`;
};

export const formatKbps = (kbps: number | null | undefined): string => {
  if (kbps === null || kbps === undefined || !Number.isFinite(kbps) || kbps <= 0) return '—';
  return kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${Math.round(kbps)} kbps`;
};

export const clamp = (value: number, min: number, max: number): number => Math.min(max, Math.max(min, value));

// Playback preferences that survive a reload: volume, mute and speed. They
// are conveniences, so a blocked or missing localStorage is simply ignored.
export interface PlayerPrefs {
  volume: number;
  muted: boolean;
  rate: number;
}

const PREFS_KEY = 'itemba.player.prefs';
const DEFAULT_PREFS: PlayerPrefs = { volume: 1, muted: false, rate: 1 };

export const loadPlayerPrefs = (): PlayerPrefs => {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) return DEFAULT_PREFS;
    const parsed = JSON.parse(raw) as Partial<PlayerPrefs>;
    const volume = typeof parsed.volume === 'number' && Number.isFinite(parsed.volume) ? clamp(parsed.volume, 0, 1) : 1;
    const rate = typeof parsed.rate === 'number' && Number.isFinite(parsed.rate) ? clamp(parsed.rate, 0.25, 4) : 1;
    return { volume, muted: parsed.muted === true, rate };
  } catch {
    return DEFAULT_PREFS;
  }
};

export const savePlayerPrefs = (prefs: PlayerPrefs): void => {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    // Storage is unavailable; the session keeps its in-memory values.
  }
};
