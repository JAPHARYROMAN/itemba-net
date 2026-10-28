import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Activity, Maximize, Maximize2, Minimize, ShieldCheck } from 'lucide-react';
import { VaultStatus } from '../types';

interface RbiViewerProps {
  vault: VaultStatus;
  latestFrame: { data: string; ts: number; url?: string } | null;
  sendWsInput: (ev: Record<string, unknown>) => void;
}

const REMOTE_WIDTH = 1280;
const REMOTE_HEIGHT = 720;

// The remote browser viewport: JPEG frames from the sandbox drawn onto a
// canvas, with pointer and keyboard input returned over the Pion data
// channel when it is open and the console socket otherwise. The HUD shows
// measured values only: frames drawn in the last second, and the age of the
// latest frame against the sandbox's own timestamp.
export const RbiViewer: React.FC<RbiViewerProps> = ({ vault, latestFrame, sendWsInput }) => {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const pcRef = useRef<RTCPeerConnection | null>(null);
  const inputDcRef = useRef<RTCDataChannel | null>(null);
  const frameCounterRef = useRef(0);
  const [rtcStatus, setRtcStatus] = useState('standby');
  const [fps, setFps] = useState(0);
  const [frameAgeMs, setFrameAgeMs] = useState<number | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [chromeVisible, setChromeVisible] = useState(true);
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false);
  const hideTimerRef = useRef<number | null>(null);

  const revealChrome = useCallback(() => {
    setChromeVisible(true);
    if (hideTimerRef.current !== null) window.clearTimeout(hideTimerRef.current);
    hideTimerRef.current = window.setTimeout(() => setChromeVisible(false), 2800);
  }, []);

  useEffect(() => () => {
    if (hideTimerRef.current !== null) window.clearTimeout(hideTimerRef.current);
  }, []);

  useEffect(() => {
    if (diagnosticsOpen) {
      if (hideTimerRef.current !== null) window.clearTimeout(hideTimerRef.current);
      setChromeVisible(true);
    } else {
      revealChrome();
    }
  }, [diagnosticsOpen, revealChrome]);

  useEffect(() => {
    const timer = setInterval(() => {
      setFps(frameCounterRef.current);
      frameCounterRef.current = 0;
    }, 1000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === rootRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  // Frames over the console socket arrive as base64 JPEG.
  useEffect(() => {
    if (!latestFrame || !latestFrame.data) return;
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext('2d');
    if (!canvas || !ctx) return;
    const img = new Image();
    img.onload = () => {
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      frameCounterRef.current += 1;
      setFrameAgeMs(Math.max(0, Date.now() - latestFrame.ts));
    };
    img.src = `data:image/jpeg;base64,${latestFrame.data}`;
  }, [latestFrame]);

  // One peer connection per vault session: an input channel and a frames
  // channel that takes over from the socket when it opens.
  useEffect(() => {
    if (!vault.active || !vault.sessionId) {
      setRtcStatus('disconnected');
      if (pcRef.current) {
        pcRef.current.close();
        pcRef.current = null;
      }
      return undefined;
    }

    let cancelled = false;
    const connect = async () => {
      try {
        setRtcStatus('negotiating');
        const pc = new RTCPeerConnection();
        pcRef.current = pc;

        const inputDc = pc.createDataChannel('itemba-input', { ordered: true });
        inputDcRef.current = inputDc;

        const framesDc = pc.createDataChannel('itemba-frames', { ordered: false, maxRetransmits: 0 });
        framesDc.binaryType = 'arraybuffer';
        framesDc.onmessage = (event) => {
          if (!(event.data instanceof ArrayBuffer)) return;
          const canvas = canvasRef.current;
          const ctx = canvas?.getContext('2d');
          if (!canvas || !ctx) return;
          const url = URL.createObjectURL(new Blob([event.data], { type: 'image/jpeg' }));
          const img = new Image();
          img.onload = () => {
            ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
            frameCounterRef.current += 1;
            URL.revokeObjectURL(url);
          };
          img.onerror = () => URL.revokeObjectURL(url);
          img.src = url;
        };

        pc.onconnectionstatechange = () => {
          if (!cancelled) setRtcStatus(pc.connectionState);
        };

        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        const res = await fetch('/api/vault/webrtc/offer', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sdp: offer.sdp }),
        });
        if (!res.ok) {
          if (!cancelled) setRtcStatus('ws-fallback');
          return;
        }
        const data = await res.json();
        if (data.sdp && !cancelled) {
          await pc.setRemoteDescription({ type: 'answer', sdp: data.sdp });
          setRtcStatus('webrtc-connected');
        }
      } catch {
        if (!cancelled) setRtcStatus('ws-fallback');
      }
    };

    void connect();
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
    if (dc && dc.readyState === 'open') dc.send(JSON.stringify(ev));
    else sendWsInput(ev);
  };

  const toRemoteCoords = (event: React.MouseEvent<HTMLCanvasElement>) => {
    const canvas = canvasRef.current;
    if (!canvas) return { x: 0, y: 0 };
    const rect = canvas.getBoundingClientRect();
    return {
      x: Math.round(((event.clientX - rect.left) * REMOTE_WIDTH) / rect.width),
      y: Math.round(((event.clientY - rect.top) * REMOTE_HEIGHT) / rect.height),
    };
  };

  const buttonName = (button: number) => (button === 2 ? 'right' : button === 1 ? 'middle' : 'left');

  const toggleFullscreen = () => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  };

  const transport = rtcStatus === 'webrtc-connected' || rtcStatus === 'connected' ? 'Pion WebRTC data channel' : 'Console socket';

  return (
    <div
      ref={rootRef}
      className={`rbi-viewer ${fullscreen ? 'rbi-viewer--fullscreen' : ''} ${chromeVisible ? '' : 'rbi-viewer--idle'} ${diagnosticsOpen ? 'rbi-viewer--diagnostics' : ''}`}
      onPointerMove={revealChrome}
      onPointerDown={revealChrome}
      onPointerLeave={() => {
        if (!diagnosticsOpen) setChromeVisible(false);
      }}
      onFocusCapture={revealChrome}
      onKeyDown={revealChrome}
    >
      <canvas
        ref={canvasRef}
        width={REMOTE_WIDTH}
        height={REMOTE_HEIGHT}
        tabIndex={0}
        className="viewport-canvas"
        aria-label={`Remote browser: ${vault.pageTitle || vault.currentUrl || 'sandbox'}`}
        onPointerDown={(event) => event.currentTarget.setPointerCapture(event.pointerId)}
        onPointerUp={(event) => event.currentTarget.releasePointerCapture(event.pointerId)}
        onMouseDown={(event) => {
          canvasRef.current?.focus();
          const { x, y } = toRemoteCoords(event);
          dispatchInput({ type: 'mousedown', x, y, button: buttonName(event.button), clickCount: 1 });
        }}
        onMouseUp={(event) => {
          const { x, y } = toRemoteCoords(event);
          dispatchInput({ type: 'mouseup', x, y, button: buttonName(event.button), clickCount: 1 });
        }}
        onMouseMove={(event) => {
          const { x, y } = toRemoteCoords(event);
          dispatchInput({ type: 'mousemove', x, y });
        }}
        onWheel={(event) => {
          const { x, y } = toRemoteCoords(event);
          dispatchInput({ type: 'wheel', x, y, deltaX: event.deltaX, deltaY: event.deltaY });
        }}
        onKeyDown={(event) => {
          // F11 and the browser's own shortcuts stay with the console.
          if (event.key === 'F11') return;
          event.preventDefault();
          dispatchInput({ type: 'keydown', key: event.key, code: event.code, keyCode: event.keyCode });
        }}
        onContextMenu={(event) => event.preventDefault()}
      />

      <div className="rbi-chrome">
        <div className="rbi-topbar">
          <div className="rbi-title">
            <ShieldCheck size={17} aria-hidden="true" />
            <div>
              <span>Protected browser</span>
              <strong>{vault.pageTitle || vault.currentUrl || 'Remote session'}</strong>
            </div>
          </div>
          <div className="rbi-top-actions">
            {vault.theaterActive && (
              <span className="rbi-theater"><Maximize2 size={12} /> DOM theater</span>
            )}
            <button
              type="button"
              className={`mp-btn rbi-chrome-button ${diagnosticsOpen ? 'mp-btn--active' : ''}`}
              aria-label="Toggle stream diagnostics"
              aria-expanded={diagnosticsOpen}
              aria-controls="rbi-stream-diagnostics"
              title="Stream diagnostics"
              onClick={() => setDiagnosticsOpen((open) => !open)}
            >
              <Activity size={18} />
            </button>
          </div>
        </div>

        {diagnosticsOpen && (
          <div className="rbi-diagnostics" id="rbi-stream-diagnostics" role="status">
            <span>{transport}</span>
            {frameAgeMs !== null && <span>Frame age {frameAgeMs} ms</span>}
            <span>{REMOTE_WIDTH}×{REMOTE_HEIGHT} · {fps} fps</span>
          </div>
        )}

        <div className="rbi-control-deck">
          <span>Click the picture to control the protected browser</span>
          <button
            type="button"
            className="mp-btn rbi-chrome-button"
            aria-label={fullscreen ? 'Exit full screen' : 'Full screen'}
            title={fullscreen ? 'Exit full screen' : 'Full screen'}
            onClick={toggleFullscreen}
          >
            {fullscreen ? <Minimize size={20} /> : <Maximize size={20} />}
          </button>
        </div>
      </div>
    </div>
  );
};
