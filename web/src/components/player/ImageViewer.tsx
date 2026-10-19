import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Loader, Maximize, Minimize, RotateCw, Scan, ShieldCheck, ShieldOff, ZoomIn, ZoomOut } from 'lucide-react';
import { ActiveRelayStream } from '../../types';
import { clamp } from '../../media/media';

interface ImageViewerProps {
  stream: ActiveRelayStream;
  src: string;
  isProtected: boolean;
  attempt: number;
  onRetryProtected: () => void;
}

interface Natural {
  width: number;
  height: number;
}

const MIN_ZOOM = 0.1;
const MAX_ZOOM = 12;
const ZOOM_STEP = 1.25;

// The zoomable image viewer. The picture is laid out at its fitted size, then
// scaled and panned with a transform; zoom keeps the point under the pointer
// still, drags pan, and the readout shows the zoom against the image's real
// pixels rather than the fitted size.
export const ImageViewer: React.FC<ImageViewerProps> = ({ stream, src, isProtected, attempt, onRetryProtected }) => {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const [natural, setNatural] = useState<Natural | null>(null);
  const [fitScale, setFitScale] = useState(1);
  const [zoom, setZoom] = useState(1);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [rotation, setRotation] = useState(0);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [fullscreen, setFullscreen] = useState(false);
  const dragRef = useRef<{ pointerId: number; startX: number; startY: number; originX: number; originY: number } | null>(null);

  useEffect(() => {
    setStatus('loading');
    setNatural(null);
    setZoom(1);
    setOffset({ x: 0, y: 0 });
    setRotation(0);
  }, [src, attempt]);

  // The fitted size follows the stage: a resize or a theater toggle re-fits.
  const measure = useCallback(() => {
    const stage = stageRef.current;
    if (!stage || !natural) return;
    const rect = stage.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const rotated = rotation % 180 !== 0;
    const w = rotated ? natural.height : natural.width;
    const h = rotated ? natural.width : natural.height;
    setFitScale(Math.min(rect.width / w, rect.height / h, 1));
  }, [natural, rotation]);

  useEffect(() => {
    measure();
    const stage = stageRef.current;
    if (!stage || typeof ResizeObserver === 'undefined') return undefined;
    const observer = new ResizeObserver(() => measure());
    observer.observe(stage);
    return () => observer.disconnect();
  }, [measure]);

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === rootRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  const zoomAround = useCallback((nextZoom: number, point?: { x: number; y: number }) => {
    const target = clamp(nextZoom, MIN_ZOOM, MAX_ZOOM);
    setZoom((current) => {
      if (point) {
        setOffset((o) => ({ x: point.x - ((point.x - o.x) * target) / current, y: point.y - ((point.y - o.y) * target) / current }));
      }
      return target;
    });
  }, []);

  const reset = () => {
    setZoom(1);
    setOffset({ x: 0, y: 0 });
  };

  const actualSize = () => {
    if (fitScale > 0) zoomAround(1 / fitScale);
  };

  const pointFromEvent = (event: { clientX: number; clientY: number }): { x: number; y: number } => {
    const stage = stageRef.current;
    if (!stage) return { x: 0, y: 0 };
    const rect = stage.getBoundingClientRect();
    return { x: event.clientX - rect.left - rect.width / 2, y: event.clientY - rect.top - rect.height / 2 };
  };

  const toggleFullscreen = () => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    let handled = true;
    switch (event.key) {
      case '+':
      case '=':
        zoomAround(zoom * ZOOM_STEP);
        break;
      case '-':
        zoomAround(zoom / ZOOM_STEP);
        break;
      case '0':
        reset();
        break;
      case '1':
        actualSize();
        break;
      case 'r':
      case 'R':
        setRotation((r) => (r + 90) % 360);
        break;
      case 'f':
      case 'F':
        toggleFullscreen();
        break;
      case 'ArrowLeft':
        setOffset((o) => ({ ...o, x: o.x + 40 }));
        break;
      case 'ArrowRight':
        setOffset((o) => ({ ...o, x: o.x - 40 }));
        break;
      case 'ArrowUp':
        setOffset((o) => ({ ...o, y: o.y + 40 }));
        break;
      case 'ArrowDown':
        setOffset((o) => ({ ...o, y: o.y - 40 }));
        break;
      default:
        handled = false;
    }
    if (handled) event.preventDefault();
  };

  // Wheel zoom must stop the page from scrolling, which needs a non-passive
  // listener; React registers wheel handlers as passive, so this one is
  // attached by hand. It reads the latest zoom and status through refs.
  const zoomRef = useRef(zoom);
  zoomRef.current = zoom;
  const statusRef = useRef(status);
  statusRef.current = status;
  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return undefined;
    const onWheel = (event: WheelEvent) => {
      if (statusRef.current !== 'ready') return;
      event.preventDefault();
      const rect = stage.getBoundingClientRect();
      const point = { x: event.clientX - rect.left - rect.width / 2, y: event.clientY - rect.top - rect.height / 2 };
      zoomAround(zoomRef.current * (event.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP), point);
    };
    stage.addEventListener('wheel', onWheel, { passive: false });
    return () => stage.removeEventListener('wheel', onWheel);
  }, [zoomAround]);

  const displayPercent = natural ? Math.round(fitScale * zoom * 100) : null;

  return (
    <div ref={rootRef} className={`mp-player mp-player--image ${fullscreen ? 'mp-player--fullscreen' : ''}`} tabIndex={0} aria-label={`Image viewer: ${stream.title}`} onKeyDown={onKeyDown}>
      <div className="mp-image-toolbar" onPointerDown={(event) => event.stopPropagation()}>
        <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
          {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
          {isProtected ? 'Protected relay' : 'Direct · filtering off'}
        </span>
        <span className="mp-controls-spacer" />
        <button type="button" className="mp-btn" aria-label="Zoom out" title="Zoom out (-)" disabled={status !== 'ready'} onClick={() => zoomAround(zoom / ZOOM_STEP)}><ZoomOut size={16} /></button>
        <span className="mp-image-zoom" aria-live="polite">{displayPercent !== null ? `${displayPercent}%` : '—'}</span>
        <button type="button" className="mp-btn" aria-label="Zoom in" title="Zoom in (+)" disabled={status !== 'ready'} onClick={() => zoomAround(zoom * ZOOM_STEP)}><ZoomIn size={16} /></button>
        <button type="button" className="mp-btn" aria-label="Fit to view" title="Fit to view (0)" disabled={status !== 'ready'} onClick={reset}><Scan size={16} /></button>
        <button type="button" className="mp-btn" aria-label="Actual pixels" title="Actual pixels (1)" disabled={status !== 'ready'} onClick={actualSize}><span className="mp-btn-caption">1:1</span></button>
        <button type="button" className="mp-btn" aria-label="Rotate" title="Rotate (r)" disabled={status !== 'ready'} onClick={() => setRotation((r) => (r + 90) % 360)}><RotateCw size={16} /></button>
        <button type="button" className="mp-btn" aria-label={fullscreen ? 'Exit full screen' : 'Full screen'} title="Full screen (f)" onClick={toggleFullscreen}>{fullscreen ? <Minimize size={16} /> : <Maximize size={16} />}</button>
      </div>

      <div
        ref={stageRef}
        className={`mp-image-stage ${dragRef.current ? 'mp-image-stage--dragging' : ''}`}
        onPointerDown={(event) => {
          if (status !== 'ready' || event.button !== 0) return;
          event.currentTarget.setPointerCapture(event.pointerId);
          dragRef.current = { pointerId: event.pointerId, startX: event.clientX, startY: event.clientY, originX: offset.x, originY: offset.y };
        }}
        onPointerMove={(event) => {
          const drag = dragRef.current;
          if (!drag || drag.pointerId !== event.pointerId) return;
          setOffset({ x: drag.originX + (event.clientX - drag.startX), y: drag.originY + (event.clientY - drag.startY) });
        }}
        onPointerUp={() => { dragRef.current = null; }}
        onPointerCancel={() => { dragRef.current = null; }}
        onDoubleClick={(event) => {
          if (status !== 'ready') return;
          if (zoom === 1) zoomAround(2.5, pointFromEvent(event));
          else reset();
        }}
      >
        {status === 'loading' && (
          <div className="mp-overlay mp-overlay--spinner"><Loader size={30} className="mp-spin" /><span>Loading through the relay…</span></div>
        )}
        {status === 'error' && (
          <div className="mp-overlay mp-overlay--error" role="alert">
            <p>{isProtected ? "The image could not load through ITEMBA's protected relay. Ad filtering remains enabled." : 'Direct image load failed. Retry through ITEMBA with ad filtering enabled.'}</p>
            <button type="button" className="btn" onClick={onRetryProtected}><RotateCw size={13} aria-hidden="true" /> Retry with ad filtering</button>
          </div>
        )}
        <img
          key={`${src}#${attempt}`}
          className="mp-image"
          src={src}
          alt={stream.title}
          draggable={false}
          style={{
            width: natural ? `${natural.width * fitScale}px` : undefined,
            height: natural ? `${natural.height * fitScale}px` : undefined,
            transform: `translate(${offset.x}px, ${offset.y}px) scale(${zoom}) rotate(${rotation}deg)`,
            visibility: status === 'ready' ? 'visible' : 'hidden',
          }}
          onLoad={(event) => {
            const img = event.currentTarget;
            setNatural({ width: img.naturalWidth, height: img.naturalHeight });
            setStatus('ready');
          }}
          onError={() => setStatus('error')}
        />
      </div>
      <div className="mp-image-foot">
        <span>{natural ? `${natural.width}×${natural.height} px` : 'Dimensions unknown'}</span>
        <span className="scout-muted">Scroll to zoom · drag to pan · double-click to toggle zoom</span>
      </div>
    </div>
  );
};
