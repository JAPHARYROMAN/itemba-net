import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Loader, Play, RotateCcw, RotateCw, X } from 'lucide-react';
import { ActiveRelayStream } from '../../types';
import { useMediaEngine } from '../../media/useMediaEngine';
import { formatKbps, formatTime, isDashUrl, isHlsUrl, relayUrl } from '../../media/media';
import { ControlBar, QueueControls } from './ControlBar';
import { MessageKey, useI18n } from '../../i18n/core';

interface VideoPlayerProps {
  stream: ActiveRelayStream;
  src: string;
  isProtected: boolean;
  attempt: number;
  autoplay: boolean;
  theater: boolean;
  headerActions?: React.ReactNode;
  onToggleTheater: () => void;
  onRetryProtected: () => void;
  onEnded?: () => void;
  queue?: QueueControls;
}

const IDLE_AFTER_MS = 2800;

const SHORTCUTS: Array<[string, MessageKey]> = [
  ['Space / k', 'player.shortcut.playPause'],
  ['j / l', 'player.shortcut.seekTen'],
  ['← / →', 'player.shortcut.seekFive'],
  ['↑ / ↓', 'player.shortcut.volume'],
  ['0 – 9', 'player.shortcut.percent'],
  ['Home / End', 'player.shortcut.startEnd'],
  [', / .', 'player.shortcut.frame'],
  ['< / >', 'player.shortcut.speed'],
  ['m', 'player.shortcut.mute'],
  ['c', 'player.shortcut.captions'],
  ['f', 'player.shortcut.fullscreen'],
  ['t', 'player.shortcut.theater'],
  ['p', 'player.shortcut.pip'],
  ['s', 'player.shortcut.stats'],
  ['?', 'player.shortcut.help'],
];

// The protected video player: a bare <video> driven by the playback engine,
// with the console's own controls drawn over it. Nothing it shows is
// estimated: resolution, frames, bitrates and buffer come from the element
// and hls.js. The path and source actions live in the immersive header, so
// the picture itself remains uncluttered while playback continues.
export const VideoPlayer: React.FC<VideoPlayerProps> = ({
  stream,
  src,
  isProtected,
  attempt,
  autoplay,
  theater,
  headerActions,
  onToggleTheater,
  onRetryProtected,
  onEnded,
  queue,
}) => {
  const { t, formatNumber } = useI18n();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [videoEl, setVideoEl] = useState<HTMLVideoElement | null>(null);
  const [idle, setIdle] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [pip, setPip] = useState(false);
  const [statsOpen, setStatsOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const idleTimer = useRef<number | null>(null);

  const dash = isDashUrl(stream.streamUrl);
  const source = useMemo(() => (dash ? null : { url: src, hls: isHlsUrl(stream.streamUrl) }), [dash, src, stream.streamUrl]);
  const failureMessage = isProtected ? t('player.error.protectedVideo') : t('player.error.directVideo');

  const { state, actions } = useMediaEngine(videoEl, source, { autoplay, attempt, failureMessage, onEnded, translate: t });

  // Controls fade while playing and the pointer is still; any activity or a
  // pause brings them back.
  const poke = useCallback(() => {
    setIdle(false);
    if (idleTimer.current !== null) window.clearTimeout(idleTimer.current);
    idleTimer.current = window.setTimeout(() => setIdle(true), IDLE_AFTER_MS);
  }, []);
  useEffect(() => {
    if (state.status !== 'playing') {
      setIdle(false);
      if (idleTimer.current !== null) window.clearTimeout(idleTimer.current);
      idleTimer.current = null;
      return undefined;
    }
    poke();
    return () => {
      if (idleTimer.current !== null) window.clearTimeout(idleTimer.current);
    };
  }, [state.status, poke]);

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === rootRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  useEffect(() => {
    if (!videoEl) return undefined;
    const onEnter = () => setPip(true);
    const onLeave = () => setPip(false);
    videoEl.addEventListener('enterpictureinpicture', onEnter);
    videoEl.addEventListener('leavepictureinpicture', onLeave);
    return () => {
      videoEl.removeEventListener('enterpictureinpicture', onEnter);
      videoEl.removeEventListener('leavepictureinpicture', onLeave);
    };
  }, [videoEl]);

  const toggleFullscreen = useCallback(() => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  }, []);

  const pipSupported = typeof document !== 'undefined' && document.pictureInPictureEnabled === true;
  const togglePip = useCallback(() => {
    if (!videoEl || !pipSupported) return;
    if (document.pictureInPictureElement === videoEl) void document.exitPictureInPicture();
    else void videoEl.requestPictureInPicture().catch(() => undefined);
  }, [videoEl, pipSupported]);

  const toggleCaptions = useCallback(() => {
    if (state.subtitleTracks.length === 0) return;
    actions.setSubtitleTrack(state.subtitleTrack >= 0 ? -1 : state.subtitleTracks[0].id);
  }, [actions, state.subtitleTrack, state.subtitleTracks]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
    if (target.tagName === 'INPUT' || target.tagName === 'SELECT' || target.tagName === 'TEXTAREA') return;
    poke();
    let handled = true;
    switch (event.key) {
      case ' ':
      case 'k':
      case 'K':
        actions.toggle();
        break;
      case 'j':
      case 'J':
        actions.seekBy(-10);
        break;
      case 'l':
      case 'L':
        actions.seekBy(10);
        break;
      case 'ArrowLeft':
        actions.seekBy(-5);
        break;
      case 'ArrowRight':
        actions.seekBy(5);
        break;
      case 'ArrowUp':
        actions.setVolume(state.volume + 0.05);
        break;
      case 'ArrowDown':
        actions.setVolume(state.volume - 0.05);
        break;
      case 'Home':
        actions.seek(0);
        break;
      case 'End':
        if (Number.isFinite(state.duration)) actions.seek(state.duration);
        break;
      case ',':
        if (state.status !== 'playing') actions.seekBy(-1 / 30);
        break;
      case '.':
        if (state.status !== 'playing') actions.seekBy(1 / 30);
        break;
      case '<':
        actions.setRate(Math.max(0.25, state.rate - 0.25));
        break;
      case '>':
        actions.setRate(Math.min(4, state.rate + 0.25));
        break;
      case 'm':
      case 'M':
        actions.toggleMute();
        break;
      case 'c':
      case 'C':
        toggleCaptions();
        break;
      case 'f':
      case 'F':
        toggleFullscreen();
        break;
      case 't':
      case 'T':
        onToggleTheater();
        break;
      case 'p':
      case 'P':
        togglePip();
        break;
      case 's':
      case 'S':
        setStatsOpen((open) => !open);
        break;
      case '?':
        setHelpOpen((open) => !open);
        break;
      case 'Escape':
        if (helpOpen || statsOpen) {
          setHelpOpen(false);
          setStatsOpen(false);
        } else {
          handled = false;
        }
        break;
      default:
        if (/^[0-9]$/.test(event.key) && Number.isFinite(state.duration)) {
          actions.seek((Number(event.key) / 10) * state.duration);
        } else {
          handled = false;
        }
    }
    if (handled) event.preventDefault();
  };

  const showBigPlay = !state.error && !dash && (state.status === 'ready' || state.status === 'paused' || state.status === 'ended' || state.autoplayBlocked);
  const showSpinner = !state.error && !dash && (state.status === 'loading' || (state.waiting && state.status === 'playing'));
  const errorMessage = dash ? t('player.error.dash') : state.error;

  return (
    <div
      ref={rootRef}
      className={`mp-player mp-player--video ${idle ? 'mp-player--idle' : ''} ${fullscreen ? 'mp-player--fullscreen' : ''}`}
      tabIndex={0}
      aria-label={t('player.aria.video', { title: stream.title })}
      onKeyDown={onKeyDown}
      onPointerMove={poke}
      onPointerDown={poke}
      onPointerLeave={() => { if (state.status === 'playing') setIdle(true); }}
    >
      <video
        ref={setVideoEl}
        className="mp-video"
        playsInline
        preload="metadata"
        poster={stream.poster}
        onClick={() => { if (!dash) actions.toggle(); }}
        onDoubleClick={toggleFullscreen}
      >
        {(stream.subtitles ?? []).map((track) => (
          <track key={track.url} kind="subtitles" label={track.label} srcLang={track.lang} src={isProtected ? relayUrl(track.url) : track.url} />
        ))}
      </video>

      <div className="mp-header">
        <div className="mp-header-copy">
          <strong title={stream.title}>{stream.title}</strong>
          <span>{stream.provider} · {stream.format}</span>
        </div>
        {headerActions && <div className="mp-header-actions">{headerActions}</div>}
      </div>

      {showSpinner && (
        <div className="mp-overlay mp-overlay--spinner" aria-live="polite">
          <Loader size={34} className="mp-spin" />
          <span>{state.status === 'loading' ? t('player.loading') : t('player.buffering')}</span>
        </div>
      )}

      {showBigPlay && (
        <button type="button" className="mp-bigplay" aria-label={state.status === 'ended' ? t('player.replay') : t('player.play')} onClick={actions.toggle}>
          {state.status === 'ended' ? <RotateCcw size={34} /> : <Play size={36} />}
          {state.autoplayBlocked && <span className="mp-bigplay-note">{t('player.autoplayBlocked')}</span>}
        </button>
      )}

      {errorMessage && (
        <div className="mp-overlay mp-overlay--error" role="alert">
          <p>{errorMessage}</p>
          {!dash && (
            <button type="button" className="btn" onClick={onRetryProtected}>
              <RotateCw size={13} aria-hidden="true" /> {t('player.retry')}
            </button>
          )}
        </div>
      )}

      {statsOpen && (
        <div className="mp-panel mp-panel--stats" aria-label={t('player.playbackStats')}>
          <div className="mp-panel-head">
            <strong>{t('player.playbackStats')}</strong>
            <button type="button" className="mp-btn" aria-label={t('player.closeStats')} onClick={() => setStatsOpen(false)}><X size={14} /></button>
          </div>
          <dl>
            <dt>{t('player.stat.path')}</dt><dd>{isProtected ? t('player.stat.relay') : t('player.stat.direct')}</dd>
            <dt>{t('player.stat.engine')}</dt><dd>{state.engine === 'hls.js' ? t('player.stat.hls') : state.engine === 'native' ? t('player.stat.native') : t('player.stat.none')}</dd>
            <dt>{t('player.stat.resolution')}</dt><dd>{state.width > 0 ? `${state.width}×${state.height}` : t('player.stat.none')}</dd>
            <dt>{t('player.stat.frames')}</dt><dd>{state.stats.totalFrames > 0 ? t('player.stat.framesValue', { decoded: formatNumber(state.stats.totalFrames), dropped: formatNumber(state.stats.droppedFrames) }) : t('player.stat.none')}</dd>
            <dt>{t('player.stat.buffer')}</dt><dd>{t('player.stat.bufferValue', { seconds: state.stats.bufferAheadSec.toFixed(1) })}</dd>
            <dt>{t('player.stat.position')}</dt><dd>{Number.isFinite(state.duration) ? t('player.of', { current: formatTime(state.currentTime), duration: formatTime(state.duration) }) : t('player.stat.positionLive', { time: formatTime(state.currentTime) })}</dd>
            <dt>{t('player.stat.speed')}</dt><dd>{state.rate}×</dd>
            {state.engine === 'hls.js' && (<>
              <dt>{t('player.stat.rendition')}</dt><dd>{state.levels.find((level) => level.id === state.activeLevel)?.label ?? t('player.stat.none')}{state.levelChoice === -1 ? t('player.stat.auto') : t('player.stat.chosen')}</dd>
              <dt>{t('player.stat.bandwidth')}</dt><dd>{formatKbps(state.stats.bandwidthKbps)}</dd>
              <dt>{t('player.stat.lastSegment')}</dt><dd>{state.stats.lastFragmentLoadMs !== null ? `${state.stats.lastFragmentLoadMs} ms` : t('player.stat.none')}</dd>
            </>)}
            <dt>{t('player.stat.source')}</dt><dd className="mp-panel-url">{stream.streamUrl}</dd>
          </dl>
        </div>
      )}

      {helpOpen && (
        <div className="mp-panel mp-panel--help" aria-label={t('player.shortcuts')}>
          <div className="mp-panel-head">
            <strong>{t('player.shortcuts')}</strong>
            <button type="button" className="mp-btn" aria-label={t('player.closeShortcuts')} onClick={() => setHelpOpen(false)}><X size={14} /></button>
          </div>
          <dl>
            {SHORTCUTS.map(([keys, what]) => (<React.Fragment key={keys}><dt><kbd>{keys}</kbd></dt><dd>{t(what)}</dd></React.Fragment>))}
          </dl>
        </div>
      )}

      <ControlBar
        state={state}
        actions={actions}
        features={{ quality: true, captions: true, audioTracks: true, pictureInPicture: pipSupported, theater: true, fullscreen: true, stats: true, help: true }}
        queue={queue}
        theater={theater}
        onToggleTheater={onToggleTheater}
        fullscreen={fullscreen}
        onToggleFullscreen={toggleFullscreen}
        pictureInPicture={pip}
        onTogglePictureInPicture={togglePip}
        statsOpen={statsOpen}
        onToggleStats={() => setStatsOpen((open) => !open)}
        helpOpen={helpOpen}
        onToggleHelp={() => setHelpOpen((open) => !open)}
      />
    </div>
  );
};
