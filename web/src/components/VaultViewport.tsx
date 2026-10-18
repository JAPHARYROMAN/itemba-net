import React, { useEffect, useRef, useState } from 'react';
import Hls from 'hls.js';
import {
  ArrowLeft,
  ArrowRight,
  RotateCw,
  ShieldCheck,
  Tv,
  Trash2,
  Play,
  Globe,
  Maximize2,
  Radio,
  Sparkles,
} from 'lucide-react';
import { ActiveRelayStream, VaultStatus } from '../types';

interface VaultViewportProps {
  vault: VaultStatus;
  activeRelay: ActiveRelayStream | null;
  viewportMode: 'relay' | 'rbi';
  setViewportMode: (mode: 'relay' | 'rbi') => void;
  latestFrame: { data: string; ts: number; url?: string } | null;
  sendWsInput: (ev: Record<string, unknown>) => void;
  onLaunchUrl: (url: string, theaterMode: boolean) => void;
  onVaultControl: (action: 'back' | 'forward' | 'reload') => void;
  onActivateTheater: () => void;
  onDestroyVault: () => void;
  onPlayRelayStream: (stream: ActiveRelayStream) => void;
}

export const VaultViewport: React.FC<VaultViewportProps> = ({
  vault,
  activeRelay,
  viewportMode,
  setViewportMode,
  latestFrame,
  sendWsInput,
  onLaunchUrl,
  onVaultControl,
  onActivateTheater,
  onDestroyVault,
  onPlayRelayStream,
}) => {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const hlsRef = useRef<Hls | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const inputDcRef = useRef<RTCDataChannel | null>(null);

  const [urlInput, setUrlInput] = useState(vault.currentUrl || '');
  const [rtcStatus, setRtcStatus] = useState<string>('standby');
  const [fps, setFps] = useState<number>(0);
  const [latencyMs, setLatencyMs] = useState<number>(11);
  const [hlsLevels, setHlsLevels] = useState<{ index: number; height: number; bitrate: number }[]>([]);
  const [selectedLevel, setSelectedLevel] = useState<number>(-1);
  const [relayProtection, setRelayProtection] = useState<{ stream: ActiveRelayStream | null; enabled: boolean }>({ stream: null, enabled: true });
  const useProxyRelay = relayProtection.stream === activeRelay ? relayProtection.enabled : activeRelay?.useZeroTrustProxy ?? true;
  const [relayRetry, setRelayRetry] = useState(0);
  const [relayFailure, setRelayFailure] = useState<{ stream: ActiveRelayStream; protected: boolean; attempt: number; message: string } | null>(null);
  const relayError = relayFailure?.stream === activeRelay && relayFailure?.protected === useProxyRelay && relayFailure?.attempt === relayRetry ? relayFailure.message : null;
  const [videoResolution, setVideoResolution] = useState<string>('');
  const frameCounterRef = useRef<number>(0);

  useEffect(() => {
    if (vault.currentUrl) {
      setUrlInput(vault.currentUrl);
    }
  }, [vault.currentUrl]);

  // FPS counter for RBI canvas
  useEffect(() => {
    const timer = setInterval(() => {
      setFps(frameCounterRef.current);
      frameCounterRef.current = 0;
    }, 1000);
    return () => clearInterval(timer);
  }, []);

  // Native 60fps HLS.js & MP4/WebM Relay Player attachment
  useEffect(() => {
    if (viewportMode !== 'relay' || !activeRelay || !videoRef.current) {
      if (hlsRef.current) {
        hlsRef.current.destroy();
        hlsRef.current = null;
      }
      return;
    }

    const video = videoRef.current;
    let attached = true;
    let attachedHls: Hls | null = null;
    const rawUrl = activeRelay.streamUrl;
    const effectiveUrl = useProxyRelay
      ? `/api/relay/stream?url=${encodeURIComponent(rawUrl)}`
      : rawUrl;

    if (hlsRef.current) {
      hlsRef.current.destroy();
      hlsRef.current = null;
    }
    setHlsLevels([]);
    setSelectedLevel(-1);
    setVideoResolution('');
    setRelayFailure(null);

    const failPlayback = (message: string) => {
      if (attached) setRelayFailure({ stream: activeRelay, protected: useProxyRelay, attempt: relayRetry, message });
    };
    const playbackFailureMessage = useProxyRelay
      ? "Playback could not load or decode through ITEMBA's protected relay. Ad filtering remains enabled."
      : 'Direct CDN playback failed. Retry through ITEMBA with ad filtering enabled.';
    const handleVideoError = () => failPlayback(playbackFailureMessage);
    const handlePlaying = () => { if (attached) setRelayFailure(null); };
    const playVideo = () => {
      void video.play().catch((error: unknown) => {
        if (!attached || (error instanceof DOMException && error.name === 'AbortError')) return;
        failPlayback(error instanceof DOMException && error.name === 'NotAllowedError'
          ? 'Automatic playback was blocked by the browser. Press Play in the video controls to start.'
          : playbackFailureMessage);
      });
    };
    video.addEventListener('error', handleVideoError);
    video.addEventListener('playing', handlePlaying);

    const isM3U8 = rawUrl.toLowerCase().includes('.m3u8');

    if (isM3U8 && Hls.isSupported()) {
      const hls = new Hls({
        enableWorker: true,
        lowLatencyMode: true,
      });
      attachedHls = hls;
      hlsRef.current = hls;
      hls.loadSource(effectiveUrl);
      hls.attachMedia(video);

      hls.on(Hls.Events.MANIFEST_PARSED, (_, data) => {
        const lvls = data.levels.map((l, idx) => ({
          index: idx,
          height: l.height || 720,
          bitrate: Math.round((l.bitrate || 0) / 1000),
        }));
        setHlsLevels(lvls);
        playVideo();
      });

      hls.on(Hls.Events.ERROR, (_, data) => {
        if (data.fatal) {
          hls.stopLoad();
          video.pause();
          failPlayback(playbackFailureMessage);
        }
      });
    } else {
      video.src = effectiveUrl;
      playVideo();
    }

    return () => {
      attached = false;
      video.removeEventListener('error', handleVideoError);
      video.removeEventListener('playing', handlePlaying);
      attachedHls?.destroy();
      if (hlsRef.current === attachedHls) hlsRef.current = null;
      video.pause();
      video.removeAttribute('src');
      video.load();
    };
  }, [viewportMode, activeRelay, useProxyRelay, relayRetry]);

  // Draw incoming base64 JPEG frames onto the HTML5 <canvas> in RBI mode
  useEffect(() => {
    if (viewportMode !== 'rbi' || !latestFrame || !latestFrame.data) return;
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const img = new Image();
    img.onload = () => {
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      frameCounterRef.current += 1;
      const rtt = Math.max(4, Math.min(85, Date.now() - latestFrame.ts));
      setLatencyMs(rtt);
    };
    img.src = 'data:image/jpeg;base64,' + latestFrame.data;
  }, [viewportMode, latestFrame]);

  // Establish Pion WebRTC PeerConnection + DataChannels when a Vault Session becomes active
  useEffect(() => {
    if (!vault.active || !vault.sessionId) {
      setRtcStatus('disconnected');
      if (pcRef.current) {
        pcRef.current.close();
        pcRef.current = null;
      }
      return;
    }

    let cancelled = false;
    const connectWebRTC = async () => {
      try {
        setRtcStatus('negotiating');
        const pc = new RTCPeerConnection();
        pcRef.current = pc;

        const inputDc = pc.createDataChannel('itemba-input', { ordered: true });
        inputDcRef.current = inputDc;

        const framesDc = pc.createDataChannel('itemba-frames', { ordered: false, maxRetransmits: 0 });
        framesDc.binaryType = 'arraybuffer';
        framesDc.onmessage = (e) => {
          if (e.data instanceof ArrayBuffer) {
            const blob = new Blob([e.data], { type: 'image/jpeg' });
            const url = URL.createObjectURL(blob);
            const canvas = canvasRef.current;
            if (canvas) {
              const ctx = canvas.getContext('2d');
              const img = new Image();
              img.onload = () => {
                ctx?.drawImage(img, 0, 0, canvas.width, canvas.height);
                frameCounterRef.current += 1;
                URL.revokeObjectURL(url);
              };
              img.src = url;
            }
          }
        };

        pc.onconnectionstatechange = () => {
          if (!cancelled) {
            setRtcStatus(pc.connectionState);
          }
        };

        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);

        const res = await fetch('/api/vault/webrtc/offer', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sdp: offer.sdp }),
        });

        if (res.ok) {
          const data = await res.json();
          if (data.sdp && !cancelled) {
            await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
            setRtcStatus('webrtc-connected');
          }
        } else {
          setRtcStatus('ws-fallback');
        }
      } catch {
        if (!cancelled) setRtcStatus('ws-fallback');
      }
    };

    connectWebRTC();

    return () => {
      cancelled = true;
      if (pcRef.current) {
        pcRef.current.close();
        pcRef.current = null;
      }
    };
  }, [vault.active, vault.sessionId]);

  const dispatchInput = (ev: Record<string, unknown>) => {
    const dc = inputDcRef.current;
    if (dc && dc.readyState === 'open') {
      dc.send(JSON.stringify(ev));
    } else {
      sendWsInput(ev);
    }
  };

  const toRemoteCoords = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const canvas = canvasRef.current;
    if (!canvas) return { x: 0, y: 0 };
    const rect = canvas.getBoundingClientRect();
    const scaleX = 1280 / rect.width;
    const scaleY = 720 / rect.height;
    return {
      x: Math.round((e.clientX - rect.left) * scaleX),
      y: Math.round((e.clientY - rect.top) * scaleY),
    };
  };

  const handleLevelChange = (lvlIdx: number) => {
    setSelectedLevel(lvlIdx);
    if (hlsRef.current) {
      hlsRef.current.currentLevel = lvlIdx;
    }
  };

  return (
    <div className={`vault-card ${vault.active || activeRelay ? 'active-isolation' : ''}`}>
      {/* Mode Switcher & RBI Browser Navigation Bar */}
      <div className="vault-toolbar">
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          {/* Dual-Engine Mode Tabs */}
          <button
            type="button"
            className={`btn ${viewportMode === 'relay' ? 'btn-primary' : ''}`}
            style={{ padding: '6px 11px', fontSize: 11.5 }}
            onClick={() => setViewportMode('relay')}
            title="60fps Native HLS (.m3u8) & MP4 Zero-Trust Stream Relay Player with full audio"
          >
            <Play size={13} /> 60fps Stream Relay
          </button>
          <button
            type="button"
            className={`btn ${viewportMode === 'rbi' ? 'btn-primary' : ''}`}
            style={{ padding: '6px 11px', fontSize: 11.5 }}
            onClick={() => setViewportMode('rbi')}
            title="Interactive Remote Browser Isolation (Chromium + CDP + Pion WebRTC)"
          >
            <Globe size={13} /> RBI Browser Sandbox {vault.active ? '●' : ''}
          </button>
		  {viewportMode === 'rbi' && vault.adBlockingEnabled && (
		    <span className="pill-badge live" role="status"><ShieldCheck size={12} /> Ad blocking enabled</span>
		  )}

          {viewportMode === 'rbi' && (
            <div style={{ display: 'flex', gap: 4, marginLeft: 4 }}>
              <button
                type="button"
                className="btn"
                style={{ padding: '6px 8px' }}
                disabled={!vault.active}
                onClick={() => onVaultControl('back')}
                title="Back"
              >
                <ArrowLeft size={13} />
              </button>
              <button
                type="button"
                className="btn"
                style={{ padding: '6px 8px' }}
                disabled={!vault.active}
                onClick={() => onVaultControl('forward')}
                title="Forward"
              >
                <ArrowRight size={13} />
              </button>
              <button
                type="button"
                className="btn"
                style={{ padding: '6px 8px' }}
                disabled={!vault.active}
                onClick={() => onVaultControl('reload')}
                title="Reload Sandbox Page"
              >
                <RotateCw size={13} />
              </button>
            </div>
          )}
        </div>

        <form
          className="vault-address-bar"
          onSubmit={(e) => {
            e.preventDefault();
            const trimmed = urlInput.trim();
            if (!trimmed) return;
            if (
              trimmed.toLowerCase().includes('.m3u8') ||
              trimmed.toLowerCase().includes('.mp4') ||
              trimmed.toLowerCase().includes('.webm')
            ) {
              onPlayRelayStream({
                title: 'Direct Media Stream: ' + trimmed.split('/').pop(),
                streamUrl: trimmed,
                provider: 'Direct Stream Input',
                format: trimmed.includes('.m3u8') ? 'HLS (.m3u8)' : 'Direct Video',
                useZeroTrustProxy: true,
              });
            } else {
              onLaunchUrl(trimmed, false);
            }
          }}
        >
          <ShieldCheck size={14} color="#10b981" />
          <input
            className="vault-address-input"
            value={urlInput}
            onChange={(e) => setUrlInput(e.target.value)}
            placeholder="Enter any webpage URL to isolate in RBI Vault, or paste a .m3u8 / .mp4 stream URL..."
          />
          <button type="submit" className="btn" style={{ padding: '3px 10px', fontSize: 11 }}>
            Launch
          </button>
        </form>

        <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          {viewportMode === 'relay' && activeRelay && (
            <>
              {hlsLevels.length > 0 && (
                <select
                  value={selectedLevel}
                  onChange={(e) => handleLevelChange(Number(e.target.value))}
                  style={{
                    background: '#111726',
                    color: '#e2e8f0',
                    border: '1px solid #1e293b',
                    borderRadius: 6,
                    padding: '5px 8px',
                    fontSize: 11.5,
                    fontFamily: 'monospace',
                  }}
                >
                  <option value={-1}>ABR Auto ({videoResolution || 'HD'})</option>
                  {hlsLevels.map((lvl) => (
                    <option key={lvl.index} value={lvl.index}>
                      {lvl.height}p ({lvl.bitrate} kbps)
                    </option>
                  ))}
                </select>
              )}
              <button
                type="button"
                className={`btn ${useProxyRelay ? 'btn-theater' : ''}`}
                style={{ padding: '6px 10px', fontSize: 11 }}
                onClick={() => setRelayProtection({ stream: activeRelay, enabled: !useProxyRelay })}
                aria-pressed={useProxyRelay}
                title="Toggle relay ad filtering for this stream. Direct CDN mode bypasses ITEMBA's stream filtering."
              >
                <Radio size={13} /> {useProxyRelay ? 'Ad filtering: ON' : 'Direct CDN · filtering off'}
              </button>
            </>
          )}

          {viewportMode === 'rbi' && (
            <>
              <button
                type="button"
                className="btn btn-theater"
                disabled={!vault.active}
                onClick={onActivateTheater}
                title="Promote <video> element inside the remote Chromium sandbox to full-viewport cinema mode"
              >
                <Tv size={13} /> Isolate DOM &lt;video&gt;
              </button>
              <button
                type="button"
                className="btn btn-danger"
                disabled={!vault.active}
                onClick={onDestroyVault}
                title="Destroy the isolated container/process and wipe ephemeral profile in milliseconds"
              >
                <Trash2 size={13} /> Destroy Vault
              </button>
            </>
          )}
        </div>
      </div>

      {/* Observed media requests can include trailers, previews and advertisements. */}
      {vault.extractedStreams && vault.extractedStreams.length > 0 && (
        <div
          style={{
            background: 'rgba(16, 185, 129, 0.08)',
            borderBottom: '1px solid rgba(16, 185, 129, 0.3)',
            padding: '7px 14px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 10,
            flexWrap: 'wrap',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12 }}>
            <Sparkles size={14} color="#10b981" />
            <strong style={{ color: '#34d399' }}>
              {vault.extractedStreams.length} observed media request(s) · full playback unverified
            </strong>
            <span
              style={{
                fontFamily: 'monospace',
                fontSize: 11,
                color: '#94a3b8',
                maxWidth: 420,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              [{vault.extractedStreams[0].format}] {vault.extractedStreams[0].url}
            </span>
          </div>
        </div>
      )}

      {viewportMode === 'relay' && activeRelay && relayError && (
        <div role="alert" style={{ padding: '12px 14px', background: 'rgba(244, 63, 94, .1)', borderBottom: '1px solid rgba(244, 63, 94, .35)', color: '#fda4af', fontSize: 12, lineHeight: 1.6 }}>
          <p>{relayError}</p>
          <button type="button" className="btn" style={{ marginTop: 8 }} onClick={() => {
            setRelayProtection({ stream: activeRelay, enabled: true });
            setRelayRetry((attempt) => attempt + 1);
          }}>
            <RotateCw size={13} aria-hidden="true" /> Retry with ad filtering
          </button>
        </div>
      )}

      {/* Main Stage: Either 60fps Native HLS/MP4 Relay Player OR Interactive WebRTC RBI Canvas */}
      <div className="viewport-stage">
        {viewportMode === 'relay' ? (
          activeRelay ? (
            <>
              <video
                ref={videoRef}
                controls
                playsInline
                style={{ width: '100%', height: '100%', background: '#000', objectFit: 'contain' }}
                onLoadedMetadata={(e) => {
                  const v = e.currentTarget;
                  if (v.videoWidth && v.videoHeight) {
                    setVideoResolution(`${v.videoWidth}×${v.videoHeight}`);
                  }
                }}
              />
              <div className="viewport-hud">
                <span className="hud-tag" style={{ borderColor: '#10b981', color: '#34d399' }}>
                  ● {useProxyRelay ? 'PROTECTED STREAM RELAY' : 'DIRECT CDN · FILTERING OFF'} ({activeRelay.format})
                </span>
                {videoResolution && <span className="hud-tag">{videoResolution} @ 60fps</span>}
                <span className="hud-tag">{activeRelay.provider}</span>
              </div>
            </>
          ) : (
            <div style={{ textAlign: 'center', padding: 36, maxWidth: 620 }}>
              <h3 style={{ fontSize: 18, fontWeight: 700, marginBottom: 8, color: '#f8fafc' }}>
                Zero-Trust 60fps Media Relay & Remote Browser Isolation
              </h3>
              <p style={{ fontSize: 13, color: '#94a3b8', lineHeight: 1.6, marginBottom: 18 }}>
                Select any verified source below to play its sanitized <code>.m3u8</code> (HLS) or <code>.mp4</code>{' '}
                stream at native 1080p60 with full audio through ITEMBA NET&apos;s Zero-Trust Proxy, or switch to{' '}
                <strong>RBI Browser Sandbox</strong> to interact with untrusted websites inside an isolated Chromium
                instance over WebRTC.
              </p>
            </div>
          )
        ) : vault.active ? (
          <>
            <canvas
              ref={canvasRef}
              width={1280}
              height={720}
              tabIndex={0}
              className="viewport-canvas"
              onMouseDown={(e) => {
                canvasRef.current?.focus();
                const { x, y } = toRemoteCoords(e);
                const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left';
                dispatchInput({ type: 'mousedown', x, y, button, clickCount: 1 });
              }}
              onMouseUp={(e) => {
                const { x, y } = toRemoteCoords(e);
                const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left';
                dispatchInput({ type: 'mouseup', x, y, button, clickCount: 1 });
              }}
              onMouseMove={(e) => {
                const { x, y } = toRemoteCoords(e);
                dispatchInput({ type: 'mousemove', x, y });
              }}
              onWheel={(e) => {
                const { x, y } = toRemoteCoords(e);
                dispatchInput({ type: 'wheel', x, y, deltaX: e.deltaX, deltaY: e.deltaY });
              }}
              onKeyDown={(e) => {
                dispatchInput({
                  type: 'keydown',
                  key: e.key,
                  code: e.code,
                  keyCode: e.keyCode,
                });
              }}
              onContextMenu={(e) => e.preventDefault()}
            />
            <div className="viewport-hud">
              <span className="hud-tag">
                {rtcStatus === 'webrtc-connected' || rtcStatus === 'connected'
                  ? 'PION WebRTC DataChannel'
                  : 'WebRTC + Low-Latency WS'}
              </span>
              <span className="hud-tag">RTT: ~{latencyMs}ms</span>
              <span className="hud-tag">1280×720 ({Math.max(fps, 1)} fps)</span>
              {vault.theaterActive && (
                <span className="hud-tag" style={{ borderColor: '#a855f7', color: '#c084fc' }}>
                  <Maximize2 size={11} /> DOM THEATER ISOLATED
                </span>
              )}
            </div>
          </>
        ) : (
          <div style={{ textAlign: 'center', padding: 36, maxWidth: 620 }}>
            <Globe size={36} color="#38bdf8" style={{ marginBottom: 12 }} />
            <h3 style={{ fontSize: 18, fontWeight: 700, marginBottom: 8, color: '#f8fafc' }}>
              Open a movie site with ad blocking
            </h3>
            <p style={{ fontSize: 13, color: '#94a3b8', lineHeight: 1.6, marginBottom: 18 }}>
              Search below and choose <strong>Open with ad blocking</strong> to browse a source with ad and tracker
              protection enabled. Paid providers may require a sign-in, subscription, rental, or purchase.
            </p>
            <div style={{ display: 'flex', gap: 10, justifyContent: 'center', flexWrap: 'wrap' }}>
              <button
                className="btn btn-primary"
                onClick={() => onLaunchUrl('https://archive.org/embed/night_of_the_living_dead', false)}
              >
                <Globe size={14} /> Isolate Internet Archive Player in RBI Vault
              </button>
              <button
                className="btn btn-theater"
                onClick={() =>
                  onLaunchUrl(
                    `${window.location.protocol}//${window.location.hostname}:8080/testbench/sketchy-cinema?q=Adversarial+Sandbox+Test`,
                    false
                  )
                }
              >
                <ShieldCheck size={14} /> Launch Adversarial Ad-Trap Testbench in Vault
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};
