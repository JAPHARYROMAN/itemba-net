import React, { useEffect, useRef, useState } from 'react';
import { FileText, Globe, Loader, Maximize, Minimize, RotateCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { ActiveRelayStream } from '../../types';
import { extensionOf, isPdfUrl } from '../../media/media';

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

// The document reader. A PDF renders in the browser's own viewer inside a
// frame fed by the relay, after the relay's answer has been checked to be a
// PDF. The frame carries no sandbox attribute and the relay sends no CSP
// sandbox for PDFs: Chrome's viewer refuses to load in a sandboxed frame or
// from a sandboxed response, so the protection is the check before framing
// plus nosniff on the relay. Every other document format is named honestly
// as not yet rendered here and offered to the protected browser instead,
// which is where the plan's Phase 3 conversion step will eventually feed from.
export const DocumentViewer: React.FC<DocumentViewerProps> = ({ stream, src, isProtected, attempt, onRetryProtected, onOpenInVault }) => {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [probe, setProbe] = useState<ProbeState>({ phase: 'probing' });
  const pdf = isPdfUrl(stream.streamUrl);
  const ext = extensionOf(stream.streamUrl).toUpperCase();

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === rootRef.current);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, []);

  useEffect(() => {
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
  }, [pdf, src, isProtected, attempt]);

  const toggleFullscreen = () => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  };

  return (
    <div ref={rootRef} className={`mp-player mp-player--document ${fullscreen ? 'mp-player--fullscreen' : ''}`} aria-label={`Document reader: ${stream.title}`}>
      <div className="mp-image-toolbar">
        <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
          {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
          {isProtected ? 'Protected relay' : 'Direct · filtering off'}
        </span>
        <span className="mp-badge"><FileText size={12} /> {ext || 'Document'}</span>
        <span className="mp-controls-spacer" />
        <button type="button" className="mp-btn" aria-label={fullscreen ? 'Exit full screen' : 'Full screen'} title="Full screen" onClick={toggleFullscreen}>{fullscreen ? <Minimize size={16} /> : <Maximize size={16} />}</button>
      </div>
      {!pdf && (
        <div className="mp-overlay mp-overlay--static">
          <FileText size={34} />
          <p>
            {ext || 'This'} documents are not rendered in the console yet. The plan converts office and e-book formats to PDF in a container before they open here;
            until then the page can be opened in the protected browser.
          </p>
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
    </div>
  );
};
