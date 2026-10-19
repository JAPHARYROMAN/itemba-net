import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Loader, Play, RotateCcw, RotateCw, ShieldCheck, ShieldOff, X } from 'lucide-react';
import { ActiveRelayStream } from '../../types';
import { useMediaEngine } from '../../media/useMediaEngine';
import { formatKbps, formatTime, isDashUrl, isHlsUrl, relayUrl } from '../../media/media';
import { ControlBar, QueueControls } from './ControlBar';

interface VideoPlayerProps {
  stream: ActiveRelayStream;
  src: string;
  isProtected: boolean;
  attempt: number;
  autoplay: boolean;
  theater: boolean;
  onToggleTheater: () => void;
  onRetryProtected: () => void;
  onEnded?: () => void;
  queue?: QueueControls;
}

const IDLE_AFTER_MS = 2800;

const SHORTCUTS: Array<[string, string]> = [
  ['Space / k', 'Play or pause'],
  ['j / l', 'Back or forward 10 seconds'],
  ['← / →', 'Back or forward 5 seconds'],
  ['↑ / ↓', 'Volume up or down'],
  ['0 – 9', 'Jump to 0% – 90%'],
  ['Home / End', 'Jump to the start or end'],
  [', / .', 'Step one frame while paused'],
  ['< / >', 'Slower or faster'],
  ['m', 'Mute'],
  ['c', 'Captions on or off'],
  ['f', 'Full screen'],
  ['t', 'Theater size'],
  ['p', 'Picture in picture'],
  ['s', 'Playback statistics'],
  ['?', 'This list'],
];

// The protected video player: a bare <video> driven by the playback engine,
// with the console's own controls drawn over it. Nothing it shows is
// estimated: resolution, frames, bitrates and buffer come from the element
// and hls.js, and the relay badge states which path the bytes take.
export const VideoPlayer: React.FC<VideoPlayerProps> = ({
  stream,
  src,
  isProtected,
  attempt,
  autoplay,
  theater,
  onToggleTheater,
  onRetryProtected,
  onEnded,
  queue,
}) => {
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
  const failureMessage = isProtected
    ? "Playback could not load or decode through ITEMBA's protected relay. Ad filtering remains enabled."
    : 'Direct CDN playback failed. Retry through ITEMBA with ad filtering enabled.';

  const { state, actions } = useMediaEngine(videoEl, source, { autoplay, attempt, failureMessage, onEnded });

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
  const errorMessage = dash
    ? 'DASH (.mpd) manifests are not supported by the console player yet. Open the page with ad blocking instead.'
    : state.error;

  return (
    <div
      ref={rootRef}
      className={`mp-player mp-player--video ${idle ? 'mp-player--idle' : ''} ${fullscreen ? 'mp-player--fullscreen' : ''}`}
      tabIndex={0}
      aria-label={`Video player: ${stream.title}`}
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

      <div className="mp-badges">
        <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
          {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
          {isProtected ? 'Protected relay · Shield on' : 'Direct CDN · filtering off'}
        </span>
        {state.engine && <span className="mp-badge">{state.engine === 'hls.js' ? 'HLS' : 'Native'}{state.width > 0 ? ` · ${state.width}×${state.height}` : ''}</span>}
      </div>

      {showSpinner && (
        <div className="mp-overlay mp-overlay--spinner" aria-live="polite">
          <Loader size={34} className="mp-spin" />
          <span>{state.status === 'loading' ? 'Loading through the relay…' : 'Buffering…'}</span>
        </div>
      )}

      {showBigPlay && (
        <button type="button" className="mp-bigplay" aria-label={state.status === 'ended' ? 'Replay' : 'Play'} onClick={actions.toggle}>
          {state.status === 'ended' ? <RotateCcw size={34} /> : <Play size={36} />}
          {state.autoplayBlocked && <span className="mp-bigplay-note">The browser blocked automatic playback. Press to start.</span>}
        </button>
      )}

      {errorMessage && (
        <div className="mp-overlay mp-overlay--error" role="alert">
          <p>{errorMessage}</p>
          {!dash && (
            <button type="button" className="btn" onClick={onRetryProtected}>
              <RotateCw size={13} aria-hidden="true" /> Retry with ad filtering
            </button>
          )}
        </div>
      )}

      {statsOpen && (
        <div className="mp-panel mp-panel--stats" aria-label="Playback statistics">
          <div className="mp-panel-head">
            <strong>Playback statistics</strong>
            <button type="button" className="mp-btn" aria-label="Close statistics" onClick={() => setStatsOpen(false)}><X size={14} /></button>
          </div>
          <dl>
            <dt>Path</dt><dd>{isProtected ? 'Relay, Shield applied to every request' : 'Direct to the CDN, no filtering'}</dd>
            <dt>Engine</dt><dd>{state.engine === 'hls.js' ? 'hls.js (MSE)' : state.engine === 'native' ? 'Browser decoder' : '—'}</dd>
            <dt>Resolution</dt><dd>{state.width > 0 ? `${state.width}×${state.height}` : '—'}</dd>
            <dt>Frames</dt><dd>{state.stats.totalFrames > 0 ? `${state.stats.totalFrames.toLocaleString()} decoded · ${state.stats.droppedFrames.toLocaleString()} dropped` : '—'}</dd>
            <dt>Buffer ahead</dt><dd>{state.stats.bufferAheadSec.toFixed(1)} s</dd>
            <dt>Position</dt><dd>{formatTime(state.currentTime)}{Number.isFinite(state.duration) ? ` of ${formatTime(state.duration)}` : ' (live)'}</dd>
            <dt>Speed</dt><dd>{state.rate}×</dd>
            {state.engine === 'hls.js' && (<>
              <dt>Rendition</dt><dd>{state.levels.find((level) => level.id === state.activeLevel)?.label ?? '—'}{state.levelChoice === -1 ? ' (auto)' : ' (chosen)'}</dd>
              <dt>Measured bandwidth</dt><dd>{formatKbps(state.stats.bandwidthKbps)}</dd>
              <dt>Last segment</dt><dd>{state.stats.lastFragmentLoadMs !== null ? `${state.stats.lastFragmentLoadMs} ms` : '—'}</dd>
            </>)}
            <dt>Source</dt><dd className="mp-panel-url">{stream.streamUrl}</dd>
          </dl>
        </div>
      )}

      {helpOpen && (
        <div className="mp-panel mp-panel--help" aria-label="Keyboard shortcuts">
          <div className="mp-panel-head">
            <strong>Keyboard shortcuts</strong>
            <button type="button" className="mp-btn" aria-label="Close shortcuts" onClick={() => setHelpOpen(false)}><X size={14} /></button>
          </div>
          <dl>
            {SHORTCUTS.map(([keys, what]) => (<React.Fragment key={keys}><dt><kbd>{keys}</kbd></dt><dd>{what}</dd></React.Fragment>))}
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
