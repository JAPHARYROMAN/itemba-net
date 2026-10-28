import { useCallback, useEffect, useRef, useState } from 'react';
import Hls from 'hls.js';
import { clamp, loadPlayerPrefs, savePlayerPrefs } from './media';
import { Translate } from '../i18n/core';

// The playback engine behind the video and audio players. It attaches one
// HTMLMediaElement to one source, through hls.js for HLS playlists and the
// browser's own decoder otherwise, and mirrors what the element and hls.js
// report into a single state object the controls render from. Every action
// goes back through the element or hls.js; nothing here is estimated.

export interface BufferedRange {
  start: number;
  end: number;
}

export interface LevelOption {
  id: number;
  width: number;
  height: number;
  bitrateKbps: number;
  label: string;
}

export interface TrackOption {
  id: number;
  label: string;
  lang?: string;
}

export interface EngineStats {
  droppedFrames: number;
  totalFrames: number;
  levelBitrateKbps: number | null;
  bandwidthKbps: number | null;
  bufferAheadSec: number;
  lastFragmentLoadMs: number | null;
}

export type EngineStatus = 'idle' | 'loading' | 'ready' | 'playing' | 'paused' | 'ended' | 'error';

export interface EngineState {
  status: EngineStatus;
  engine: 'hls.js' | 'native' | null;
  waiting: boolean;
  currentTime: number;
  duration: number;
  live: boolean;
  buffered: BufferedRange[];
  volume: number;
  muted: boolean;
  rate: number;
  width: number;
  height: number;
  levels: LevelOption[];
  levelChoice: number;
  activeLevel: number;
  audioTracks: TrackOption[];
  audioTrack: number;
  subtitleTracks: TrackOption[];
  subtitleTrack: number;
  stats: EngineStats;
  error: string | null;
  autoplayBlocked: boolean;
}

export interface EngineActions {
  play: () => void;
  pause: () => void;
  toggle: () => void;
  seek: (seconds: number) => void;
  seekBy: (delta: number) => void;
  setVolume: (volume: number) => void;
  setMuted: (muted: boolean) => void;
  toggleMute: () => void;
  setRate: (rate: number) => void;
  setLevel: (id: number) => void;
  setAudioTrack: (id: number) => void;
  setSubtitleTrack: (id: number) => void;
}

export interface EngineSource {
  url: string;
  hls: boolean;
}

export interface EngineOptions {
  autoplay: boolean;
  // Bumped by the caller to re-attach the same source after a failure.
  attempt: number;
  // The message shown for a load or decode failure; the caller knows whether
  // the bytes came through the protected relay or straight from a CDN.
  failureMessage: string;
  // The interface's translator, so error suffixes and fallback labels follow
  // the chosen locale.
  translate: Translate;
  onEnded?: () => void;
}

const INITIAL_STATS: EngineStats = {
  droppedFrames: 0,
  totalFrames: 0,
  levelBitrateKbps: null,
  bandwidthKbps: null,
  bufferAheadSec: 0,
  lastFragmentLoadMs: null,
};

const INITIAL_STATE: EngineState = {
  status: 'idle',
  engine: null,
  waiting: false,
  currentTime: 0,
  duration: NaN,
  live: false,
  buffered: [],
  volume: 1,
  muted: false,
  rate: 1,
  width: 0,
  height: 0,
  levels: [],
  levelChoice: -1,
  activeLevel: -1,
  audioTracks: [],
  audioTrack: -1,
  subtitleTracks: [],
  subtitleTrack: -1,
  stats: INITIAL_STATS,
  error: null,
  autoplayBlocked: false,
};

const describeMediaError = (element: HTMLMediaElement, fallback: string, t: Translate): string => {
  const code = element.error?.code;
  switch (code) {
    case MediaError.MEDIA_ERR_NETWORK:
      return `${fallback}${t('player.error.network')}`;
    case MediaError.MEDIA_ERR_DECODE:
      return `${fallback}${t('player.error.decode')}`;
    case MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED:
      return `${fallback}${t('player.error.unsupported')}`;
    default:
      return fallback;
  }
};

const readBuffered = (element: HTMLMediaElement): BufferedRange[] => {
  const ranges: BufferedRange[] = [];
  const { buffered } = element;
  for (let i = 0; i < buffered.length; i += 1) {
    ranges.push({ start: buffered.start(i), end: buffered.end(i) });
  }
  return ranges;
};

const bufferAheadOf = (element: HTMLMediaElement): number => {
  const t = element.currentTime;
  const { buffered } = element;
  for (let i = 0; i < buffered.length; i += 1) {
    if (buffered.start(i) <= t && t <= buffered.end(i)) return Math.max(0, buffered.end(i) - t);
  }
  return 0;
};

const levelLabel = (height: number, bitrateKbps: number, t: Translate): string => {
  const res = height > 0 ? `${height}p` : t('player.unknownSize');
  return bitrateKbps > 0 ? `${res} · ${bitrateKbps >= 1000 ? `${(bitrateKbps / 1000).toFixed(1)} Mbps` : `${bitrateKbps} kbps`}` : res;
};

const trackLabel = (name: string | undefined, lang: string | undefined, index: number, t: Translate): string =>
  name?.trim() || (lang ? lang.toUpperCase() : t('player.trackFallback', { index: index + 1 }));

const nativeTextTracks = (element: HTMLMediaElement, t: Translate): TrackOption[] => {
  const options: TrackOption[] = [];
  const tracks = element.textTracks;
  for (let i = 0; i < tracks.length; i += 1) {
    const track = tracks[i];
    if (track.kind !== 'subtitles' && track.kind !== 'captions') continue;
    options.push({ id: i, label: trackLabel(track.label, track.language, i, t), lang: track.language || undefined });
  }
  return options;
};

export const useMediaEngine = (
  element: HTMLMediaElement | null,
  source: EngineSource | null,
  options: EngineOptions,
): { state: EngineState; actions: EngineActions } => {
  const [state, setState] = useState<EngineState>(INITIAL_STATE);
  const hlsRef = useRef<Hls | null>(null);
  // Which subtitle list the engine exposes: hls.js's own when the playlist
  // declares any, otherwise the element's <track> children.
  const subtitleModeRef = useRef<'hls' | 'native'>('native');
  const optionsRef = useRef(options);
  optionsRef.current = options;

  // The latest state for the hls.js callbacks, which close over the attach
  // effect and would otherwise read a stale stats object.
  const stateRef = useRef(state);
  stateRef.current = state;

  const patch = useCallback((changes: Partial<EngineState>) => {
    setState((previous) => ({ ...previous, ...changes }));
  }, []);

  useEffect(() => {
    if (!element || !source) {
      setState(INITIAL_STATE);
      return undefined;
    }

    let disposed = false;
    const cleanups: Array<() => void> = [];
    const prefs = loadPlayerPrefs();
    element.volume = prefs.volume;
    element.muted = prefs.muted;
    element.defaultPlaybackRate = prefs.rate;
    element.playbackRate = prefs.rate;
    subtitleModeRef.current = 'native';

    setState({
      ...INITIAL_STATE,
      status: 'loading',
      engine: source.hls && Hls.isSupported() ? 'hls.js' : 'native',
      volume: prefs.volume,
      muted: prefs.muted,
      rate: prefs.rate,
    });

    const safePatch = (changes: Partial<EngineState>) => {
      if (!disposed) patch(changes);
    };
    const fail = (message: string) => safePatch({ status: 'error', error: message, waiting: false });

    const tryAutoplay = () => {
      if (!optionsRef.current.autoplay) return;
      element.play().catch((error: unknown) => {
        if (disposed || (error instanceof DOMException && error.name === 'AbortError')) return;
        if (error instanceof DOMException && error.name === 'NotAllowedError') {
          safePatch({ autoplayBlocked: true, status: 'paused' });
          return;
        }
        fail(optionsRef.current.failureMessage);
      });
    };

    const on = <K extends keyof HTMLMediaElementEventMap>(name: K, handler: (event: HTMLMediaElementEventMap[K]) => void) => {
      element.addEventListener(name, handler);
      cleanups.push(() => element.removeEventListener(name, handler));
    };

    const videoSize = () => {
      const video = element as HTMLVideoElement;
      return { width: video.videoWidth || 0, height: video.videoHeight || 0 };
    };

    const refreshNativeSubtitles = () => {
      if (subtitleModeRef.current !== 'native') return;
      const tracks = nativeTextTracks(element, optionsRef.current.translate);
      let showing = -1;
      for (let i = 0; i < element.textTracks.length; i += 1) {
        if (element.textTracks[i].mode === 'showing') showing = i;
      }
      safePatch({ subtitleTracks: tracks, subtitleTrack: showing });
    };

    on('loadedmetadata', () => {
      safePatch({
        duration: element.duration,
        live: !Number.isFinite(element.duration),
        ...videoSize(),
      });
      // Side-loaded <track> children start disabled: captions are a choice.
      for (let i = 0; i < element.textTracks.length; i += 1) element.textTracks[i].mode = 'disabled';
      refreshNativeSubtitles();
    });
    on('durationchange', () => safePatch({ duration: element.duration, live: !Number.isFinite(element.duration) }));
    on('resize', () => safePatch(videoSize()));
    on('canplay', () => {
      safePatch({ waiting: false });
      setState((previous) => (previous.status === 'loading' ? { ...previous, status: element.paused ? 'ready' : 'playing' } : previous));
    });
    on('play', () => safePatch({ status: 'playing', autoplayBlocked: false, error: null }));
    on('playing', () => safePatch({ status: 'playing', waiting: false, error: null }));
    on('pause', () => {
      if (!element.ended) safePatch({ status: 'paused' });
    });
    on('ended', () => {
      safePatch({ status: 'ended', waiting: false });
      optionsRef.current.onEnded?.();
    });
    on('waiting', () => safePatch({ waiting: true }));
    on('seeking', () => safePatch({ waiting: true, currentTime: element.currentTime }));
    on('seeked', () => safePatch({ waiting: false, currentTime: element.currentTime, buffered: readBuffered(element) }));
    on('timeupdate', () => safePatch({ currentTime: element.currentTime, buffered: readBuffered(element) }));
    on('progress', () => safePatch({ buffered: readBuffered(element) }));
    on('volumechange', () => {
      safePatch({ volume: element.volume, muted: element.muted });
      savePlayerPrefs({ volume: element.volume, muted: element.muted, rate: element.playbackRate });
    });
    on('ratechange', () => {
      safePatch({ rate: element.playbackRate });
      savePlayerPrefs({ volume: element.volume, muted: element.muted, rate: element.playbackRate });
    });
    on('error', () => fail(describeMediaError(element, optionsRef.current.failureMessage, optionsRef.current.translate)));

    const textTracks = element.textTracks;
    const onTracksChanged = () => refreshNativeSubtitles();
    textTracks.addEventListener('addtrack', onTracksChanged);
    textTracks.addEventListener('removetrack', onTracksChanged);
    textTracks.addEventListener('change', onTracksChanged);
    cleanups.push(() => {
      textTracks.removeEventListener('addtrack', onTracksChanged);
      textTracks.removeEventListener('removetrack', onTracksChanged);
      textTracks.removeEventListener('change', onTracksChanged);
    });

    let hls: Hls | null = null;
    if (source.hls && Hls.isSupported()) {
      hls = new Hls({ enableWorker: true, backBufferLength: 90 });
      hlsRef.current = hls;

      hls.on(Hls.Events.MANIFEST_PARSED, (_event, data) => {
        const levels: LevelOption[] = data.levels.map((level, index) => {
          const bitrateKbps = Math.round((level.bitrate || 0) / 1000);
          return { id: index, width: level.width || 0, height: level.height || 0, bitrateKbps, label: levelLabel(level.height || 0, bitrateKbps, optionsRef.current.translate) };
        });
        safePatch({ levels, levelChoice: -1 });
        tryAutoplay();
      });
      hls.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => {
        const level = hls?.levels[data.level];
        safePatch({ activeLevel: data.level, stats: { ...stateRef.current.stats, levelBitrateKbps: level ? Math.round(level.bitrate / 1000) : null } });
      });
      hls.on(Hls.Events.AUDIO_TRACKS_UPDATED, (_event, data) => {
        safePatch({
          audioTracks: data.audioTracks.map((track, index) => ({ id: track.id, label: trackLabel(track.name, track.lang, index, optionsRef.current.translate), lang: track.lang || undefined })),
          audioTrack: hls?.audioTrack ?? -1,
        });
      });
      hls.on(Hls.Events.AUDIO_TRACK_SWITCHED, (_event, data) => safePatch({ audioTrack: data.id }));
      hls.on(Hls.Events.SUBTITLE_TRACKS_UPDATED, (_event, data) => {
        if (data.subtitleTracks.length === 0) {
          subtitleModeRef.current = 'native';
          refreshNativeSubtitles();
          return;
        }
        subtitleModeRef.current = 'hls';
        if (hls) hls.subtitleDisplay = false;
        safePatch({
          subtitleTracks: data.subtitleTracks.map((track, index) => ({ id: track.id, label: trackLabel(track.name, track.lang, index, optionsRef.current.translate), lang: track.lang || undefined })),
          subtitleTrack: -1,
        });
      });
      hls.on(Hls.Events.SUBTITLE_TRACK_SWITCH, (_event, data) => {
        if (subtitleModeRef.current === 'hls') safePatch({ subtitleTrack: hls?.subtitleDisplay ? data.id : -1 });
      });
      hls.on(Hls.Events.FRAG_BUFFERED, (_event, data) => {
        const loading = data.frag.stats?.loading;
        if (loading && loading.end > 0 && loading.start > 0) {
          safePatch({ stats: { ...stateRef.current.stats, lastFragmentLoadMs: Math.round(loading.end - loading.start) } });
        }
      });
      hls.on(Hls.Events.ERROR, (_event, data) => {
        if (!data.fatal) return;
        hls?.stopLoad();
        element.pause();
        const detail = data.type === Hls.ErrorTypes.NETWORK_ERROR
          ? optionsRef.current.translate('player.error.hlsNetwork')
          : data.type === Hls.ErrorTypes.MEDIA_ERROR
            ? optionsRef.current.translate('player.error.hlsMedia')
            : '';
        fail(`${optionsRef.current.failureMessage}${detail}`);
      });

      hls.loadSource(source.url);
      hls.attachMedia(element);
    } else {
      element.src = source.url;
      element.load();
      tryAutoplay();
    }

    const statsTimer = window.setInterval(() => {
      if (disposed) return;
      const video = element as HTMLVideoElement;
      const quality = typeof video.getVideoPlaybackQuality === 'function' ? video.getVideoPlaybackQuality() : null;
      const activeHls = hlsRef.current;
      const activeLevel = activeHls && activeHls.currentLevel >= 0 ? activeHls.levels[activeHls.currentLevel] : null;
      const bandwidth = activeHls?.bandwidthEstimate;
      setState((previous) => ({
        ...previous,
        stats: {
          ...previous.stats,
          droppedFrames: quality?.droppedVideoFrames ?? 0,
          totalFrames: quality?.totalVideoFrames ?? 0,
          bufferAheadSec: bufferAheadOf(element),
          levelBitrateKbps: activeLevel ? Math.round(activeLevel.bitrate / 1000) : previous.stats.levelBitrateKbps,
          bandwidthKbps: typeof bandwidth === 'number' && Number.isFinite(bandwidth) && bandwidth > 0 ? Math.round(bandwidth / 1000) : null,
        },
      }));
    }, 1000);

    return () => {
      disposed = true;
      window.clearInterval(statsTimer);
      cleanups.forEach((undo) => undo());
      if (hls) {
        hls.destroy();
        if (hlsRef.current === hls) hlsRef.current = null;
      }
      element.pause();
      element.removeAttribute('src');
      element.load();
    };
  }, [element, source?.url, source?.hls, options.attempt, patch]);

  const play = useCallback(() => {
    if (!element) return;
    element.play().catch((error: unknown) => {
      if (error instanceof DOMException && error.name === 'AbortError') return;
      if (error instanceof DOMException && error.name === 'NotAllowedError') {
        patch({ autoplayBlocked: true });
        return;
      }
      patch({ status: 'error', error: optionsRef.current.failureMessage });
    });
  }, [element, patch]);

  const pause = useCallback(() => element?.pause(), [element]);

  const toggle = useCallback(() => {
    if (!element) return;
    if (element.paused || element.ended) play();
    else element.pause();
  }, [element, play]);

  const seek = useCallback((seconds: number) => {
    if (!element) return;
    const limit = Number.isFinite(element.duration) ? element.duration : Number.POSITIVE_INFINITY;
    const target = clamp(seconds, 0, limit);
    element.currentTime = target;
    patch({ currentTime: target });
  }, [element, patch]);

  const seekBy = useCallback((delta: number) => {
    if (element) seek(element.currentTime + delta);
  }, [element, seek]);

  const setVolume = useCallback((volume: number) => {
    if (!element) return;
    element.volume = clamp(volume, 0, 1);
    if (element.volume > 0 && element.muted) element.muted = false;
  }, [element]);

  const setMuted = useCallback((muted: boolean) => {
    if (element) element.muted = muted;
  }, [element]);

  const toggleMute = useCallback(() => {
    if (element) element.muted = !element.muted;
  }, [element]);

  const setRate = useCallback((rate: number) => {
    if (!element) return;
    element.playbackRate = clamp(rate, 0.25, 4);
  }, [element]);

  const setLevel = useCallback((id: number) => {
    const hls = hlsRef.current;
    if (!hls) return;
    hls.currentLevel = id;
    patch({ levelChoice: id });
  }, [patch]);

  const setAudioTrack = useCallback((id: number) => {
    const hls = hlsRef.current;
    if (hls) hls.audioTrack = id;
  }, []);

  const setSubtitleTrack = useCallback((id: number) => {
    if (subtitleModeRef.current === 'hls') {
      const hls = hlsRef.current;
      if (!hls) return;
      hls.subtitleDisplay = id >= 0;
      if (id >= 0) hls.subtitleTrack = id;
      patch({ subtitleTrack: id });
      return;
    }
    if (!element) return;
    for (let i = 0; i < element.textTracks.length; i += 1) {
      const track = element.textTracks[i];
      if (track.kind !== 'subtitles' && track.kind !== 'captions') continue;
      track.mode = i === id ? 'showing' : 'disabled';
    }
    patch({ subtitleTrack: id });
  }, [element, patch]);

  return {
    state,
    actions: { play, pause, toggle, seek, seekBy, setVolume, setMuted, toggleMute, setRate, setLevel, setAudioTrack, setSubtitleTrack },
  };
};
