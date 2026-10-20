import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Headphones, RotateCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { ActiveRelayStream } from '../../types';
import { useMediaEngine } from '../../media/useMediaEngine';
import { isHlsUrl } from '../../media/media';
import { ControlBar, QueueControls } from './ControlBar';

interface AudioPlayerProps {
  stream: ActiveRelayStream;
  src: string;
  isProtected: boolean;
  attempt: number;
  autoplay: boolean;
  onRetryProtected: () => void;
  onEnded?: () => void;
  queue?: QueueControls;
}

// One analyser per element: a media element can be wired into the Web
// Audio graph exactly once, and this player keeps the element across
// re-attachments of the same source.
const analysers = new WeakMap<HTMLMediaElement, { context: AudioContext; analyser: AnalyserNode }>();

const BAR_COUNT = 48;

// The audio player: the same engine and control bar as video, with the
// artwork and a level meter in place of the picture. The meter only taps
// same-origin (relayed) audio: a cross-origin element without CORS would fall
// silent once routed through the Web Audio graph, so direct playback is left
// untouched and simply has no meter.
export const AudioPlayer: React.FC<AudioPlayerProps> = ({ stream, src, isProtected, attempt, autoplay, onRetryProtected, onEnded, queue }) => {
  const [audioEl, setAudioEl] = useState<HTMLAudioElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [statsOpen, setStatsOpen] = useState(false);

  const source = useMemo(() => ({ url: src, hls: isHlsUrl(stream.streamUrl) }), [src, stream.streamUrl]);
  const failureMessage = isProtected
    ? "This audio could not load or decode through ITEMBA's protected relay. Ad filtering remains enabled."
    : 'Direct CDN playback failed. Retry through ITEMBA with ad filtering enabled.';
  const { state, actions } = useMediaEngine(audioEl, source, { autoplay, attempt, failureMessage, onEnded });

  const meterable = isProtected && src.startsWith('/');

  useEffect(() => {
    if (!audioEl || !meterable || state.status !== 'playing') return undefined;
    const canvas = canvasRef.current;
    if (!canvas) return undefined;
    let wiring = analysers.get(audioEl);
    if (!wiring) {
      try {
        const context = new AudioContext();
        const analyser = context.createAnalyser();
        analyser.fftSize = 256;
        analyser.smoothingTimeConstant = 0.82;
        context.createMediaElementSource(audioEl).connect(analyser);
        analyser.connect(context.destination);
        wiring = { context, analyser };
        analysers.set(audioEl, wiring);
      } catch {
        return undefined;
      }
    }
    const { context, analyser } = wiring;
    if (context.state === 'suspended') void context.resume().catch(() => undefined);
    const data = new Uint8Array(analyser.frequencyBinCount);
    const ctx = canvas.getContext('2d');
    if (!ctx) return undefined;
    let frame = 0;
    const draw = () => {
      frame = requestAnimationFrame(draw);
      analyser.getByteFrequencyData(data);
      const { width, height } = canvas;
      ctx.clearRect(0, 0, width, height);
      const gap = 3;
      const barWidth = (width - gap * (BAR_COUNT - 1)) / BAR_COUNT;
      const step = Math.max(1, Math.floor(data.length / BAR_COUNT));
      for (let i = 0; i < BAR_COUNT; i += 1) {
        let sum = 0;
        for (let j = 0; j < step; j += 1) sum += data[i * step + j] ?? 0;
        const level = sum / step / 255;
        const barHeight = Math.max(2, level * height);
        const x = i * (barWidth + gap);
        const gradient = ctx.createLinearGradient(0, height - barHeight, 0, height);
        gradient.addColorStop(0, '#38bdf8');
        gradient.addColorStop(1, '#10b981');
        ctx.fillStyle = gradient;
        ctx.fillRect(x, height - barHeight, barWidth, barHeight);
      }
    };
    draw();
    return () => cancelAnimationFrame(frame);
  }, [audioEl, meterable, state.status]);

  return (
    <div className="mp-player mp-player--audio" aria-label={`Audio player: ${stream.title}`}>
      {/* The element is keyed by path so a direct retry gets an element the audio graph has never touched. */}
      <audio key={isProtected ? 'relay' : 'direct'} ref={setAudioEl} preload="metadata" />
      {/* The next queued track loads its beginning now, muted and silent, so the
          switch at the end of this one serves from the browser's media cache
          instead of a cold request: near-gapless, not sample-accurate. */}
      {queue?.nextSrc && <audio key={`next:${queue.nextSrc}`} src={queue.nextSrc} preload="auto" muted aria-hidden="true" />}
      <div className="mp-audio-body">
        <div className="mp-audio-art">
          {stream.poster ? <img src={stream.poster} alt="" /> : <Headphones size={42} />}
        </div>
        <div className="mp-audio-meta">
          <strong>{stream.title}</strong>
          <span className="scout-muted">{stream.provider} · {stream.format}{queue && queue.length > 1 ? ` · track ${queue.position + 1} of ${queue.length}` : ''}</span>
          <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
            {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
            {isProtected ? 'Protected relay · Shield on' : 'Direct CDN · filtering off'}
          </span>
          {state.error && (
            <div className="mp-audio-error" role="alert">
              <p>{state.error}</p>
              <button type="button" className="btn" onClick={onRetryProtected}><RotateCw size={13} aria-hidden="true" /> Retry with ad filtering</button>
            </div>
          )}
        </div>
        <div className="mp-audio-meter" aria-hidden="true">
          {meterable ? <canvas ref={canvasRef} width={480} height={96} /> : <span className="scout-muted">Level meter is only available through the relay.</span>}
        </div>
      </div>
      {statsOpen && (
        <dl className="mp-audio-stats">
          <dt>Engine</dt><dd>{state.engine === 'hls.js' ? 'hls.js (MSE)' : state.engine === 'native' ? 'Browser decoder' : '—'}</dd>
          <dt>Buffer ahead</dt><dd>{state.stats.bufferAheadSec.toFixed(1)} s</dd>
          <dt>Source</dt><dd className="mp-panel-url">{stream.streamUrl}</dd>
        </dl>
      )}
      <ControlBar
        state={state}
        actions={actions}
        features={{ audioTracks: true, stats: true }}
        queue={queue}
        statsOpen={statsOpen}
        onToggleStats={() => setStatsOpen((open) => !open)}
      />
    </div>
  );
};
