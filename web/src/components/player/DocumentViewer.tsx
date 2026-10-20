import React, { useEffect, useRef, useState } from 'react';
import { FileText, Globe, Loader, Maximize, Minimize, RefreshCw, RotateCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { ActiveRelayStream, ConvertStatus } from '../../types';
import { convertUrl, extensionOf, isConvertibleUrl, isPdfUrl } from '../../media/media';

interface DocumentViewerProps {
  stream: ActiveRelayStream;
  src: string;
  isProtected: boolean;
  attempt: number;
  onRetryProtected: () => void;
  onOpenInVault: (url: string) => void;
}

type ProbeState =
  | { phase: 'probing' }
  | { phase: 'pdf' }
  | { phase: 'converting' }
  | { phase: 'converted'; status: ConvertStatus }
  | { phase: 'unconvertible'; message: string }
  | { phase: 'refused'; message: string };

// Asks the relay for the first byte of the document and reads what it
// actually answers. A PDF is framed; anything else, such as a provider's
// rate-limit or sign-in page, is reported as the response it was and never
// rendered. Only the relay is probed: it is same-origin, so the answer is
// readable, whereas a CDN in direct mode usually withholds CORS headers.
const probeDocument = async (src: string, signal: AbortSignal): Promise<ProbeState> => {
  try {
    const response = await fetch(src, { headers: { Range: 'bytes=0-0' }, signal });
    const type = (response.headers.get('Content-Type') ?? '').toLowerCase();
    if (!response.ok) {
      const status = `${response.status} ${response.statusText}`.trim();
      return { phase: 'refused', message: `The source answered ${status} through ITEMBA's protected relay instead of the document. Ad filtering remains enabled.` };
    }
    if (!type.startsWith('application/pdf')) {
      return { phase: 'refused', message: `The source answered with ${type.split(';')[0] || 'an unknown type'} through ITEMBA's protected relay, not a PDF. It is not rendered.` };
    }
    return { phase: 'pdf' };
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return { phase: 'probing' };
    return { phase: 'refused', message: "The document could not be fetched through ITEMBA's protected relay. Ad filtering remains enabled." };
  }
};

// Asks the console to convert an e-book, a DjVu scan or an office file to
// a PDF and reads what it says: converted (by which tool, in how long), or
// why not, such as a host with no converter.
const convertDocument = async (streamUrl: string, signal: AbortSignal): Promise<ProbeState> => {
  try {
    const response = await fetch(`${convertUrl(streamUrl)}&info=1`, { signal });
    const status = (await response.json()) as ConvertStatus;
    if (response.ok && status.converted) return { phase: 'converted', status };
    return { phase: 'unconvertible', message: status.error || `The console could not convert this ${status.format?.toUpperCase() || 'file'} to a PDF.` };
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return { phase: 'converting' };
    return { phase: 'unconvertible', message: 'The console could not be asked to convert the document.' };
  }
};

// The document reader. A PDF renders in the browser's own viewer inside a
// frame fed by the relay, after the relay's answer has been checked to be a
// PDF. The frame carries no sandbox attribute and the relay sends no CSP
// sandbox for PDFs: Chrome's viewer refuses to load in a sandboxed frame or
// from a sandboxed response, so the protection is the check before framing
// plus nosniff on the relay. An e-book, a DjVu scan or an office file is
// converted to a PDF by the console, which fetches it through Shield and
// runs a converter this host has, and the converted PDF is framed the same
// way; when this host has no converter the reader says so and offers the
// protected browser instead. Every other format is named honestly as not
// rendered here.
export const DocumentViewer: React.FC<DocumentViewerProps> = ({ stream, src, isProtected, attempt, onRetryProtected, onOpenInVault }) => {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [probe, setProbe] = useState<ProbeState>({ phase: 'probing' });
  const pdf = isPdfUrl(stream.streamUrl);
  const convertible = !pdf && isConvertibleUrl(stream.streamUrl);
  const ext = extensionOf(stream.streamUrl).toUpperCase();

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === rootRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  useEffect(() => {
    if (convertible) {
      const controller = new AbortController();
      setProbe({ phase: 'converting' });
      void convertDocument(stream.streamUrl, controller.signal).then((result) => {
        if (!controller.signal.aborted) setProbe(result);
      });
      return () => controller.abort();
    }
    if (!pdf) return undefined;
    if (!isProtected) {
      // Direct mode frames the CDN address as it is; the operator chose the
      // unfiltered path and a cross-origin answer cannot be read here.
      setProbe({ phase: 'pdf' });
      return undefined;
    }
    const controller = new AbortController();
    setProbe({ phase: 'probing' });
    void probeDocument(src, controller.signal).then((result) => {
      if (!controller.signal.aborted) setProbe(result);
    });
    return () => controller.abort();
  }, [pdf, convertible, stream.streamUrl, src, isProtected, attempt]);

  const toggleFullscreen = () => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  };

  const conversionLabel = probe.phase === 'converted'
    ? `Converted from ${ext} by ${probe.status.converter ?? 'the host'}${probe.status.cached ? ' earlier' : probe.status.seconds ? ` in ${probe.status.seconds.toFixed(1)} s` : ''}`
    : null;

  return (
    <div ref={rootRef} className={`mp-player mp-player--document ${fullscreen ? 'mp-player--fullscreen' : ''}`} aria-label={`Document reader: ${stream.title}`}>
      <div className="mp-image-toolbar">
        <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
          {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
          {isProtected ? 'Protected relay' : 'Direct · filtering off'}
        </span>
        <span className="mp-badge"><FileText size={12} /> {ext || 'Document'}</span>
        {conversionLabel && <span className="mp-badge"><RefreshCw size={12} /> {conversionLabel}</span>}
        <span className="mp-controls-spacer" />
        <button type="button" className="mp-btn" aria-label={fullscreen ? 'Exit full screen' : 'Full screen'} title="Full screen" onClick={toggleFullscreen}>{fullscreen ? <Minimize size={16} /> : <Maximize size={16} />}</button>
      </div>
      {!pdf && !convertible && (
        <div className="mp-overlay mp-overlay--static">
          <FileText size={34} />
          <p>
            {ext || 'This'} documents are not rendered in the console. E-books, DjVu scans and office files are converted to PDF by the console when this host has a converter;
            this format is not one of them, so the page can be opened in the protected browser instead.
          </p>
          <button type="button" className="btn btn-primary" onClick={() => onOpenInVault(stream.pageUrl ?? stream.streamUrl)}>
            <Globe size={14} aria-hidden="true" /> Open with ad blocking
          </button>
        </div>
      )}
      {probe.phase === 'converting' && (
        <div className="mp-overlay mp-overlay--spinner"><Loader size={30} className="mp-spin" /><span>Converting the {ext} to a PDF on the console…</span></div>
      )}
      {probe.phase === 'unconvertible' && (
        <div className="mp-overlay mp-overlay--static" role="alert">
          <FileText size={34} />
          <p>{probe.message}</p>
          <button type="button" className="btn btn-primary" onClick={() => onOpenInVault(stream.pageUrl ?? stream.streamUrl)}>
            <Globe size={14} aria-hidden="true" /> Open with ad blocking
          </button>
        </div>
      )}
      {pdf && probe.phase === 'probing' && (
        <div className="mp-overlay mp-overlay--spinner"><Loader size={30} className="mp-spin" /><span>Checking the document through the relay…</span></div>
      )}
      {pdf && probe.phase === 'refused' && (
        <div className="mp-overlay mp-overlay--error" role="alert">
          <p>{probe.message}</p>
          <button type="button" className="btn" onClick={onRetryProtected}><RotateCw size={13} aria-hidden="true" /> Retry with ad filtering</button>
        </div>
      )}
      {pdf && probe.phase === 'pdf' && (
        <iframe className="mp-document-frame" src={src} title={stream.title} />
      )}
      {probe.phase === 'converted' && (
        <iframe className="mp-document-frame" src={convertUrl(stream.streamUrl)} title={stream.title} />
      )}
    </div>
  );
};
