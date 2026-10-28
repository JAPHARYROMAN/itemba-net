import React, { useEffect, useRef, useState } from 'react';
import { FileText, Globe, Loader, Maximize, Minimize, RefreshCw, RotateCw, ShieldCheck, ShieldOff } from 'lucide-react';
import { ActiveRelayStream, ConvertStatus } from '../../types';
import { convertUrl, extensionOf, isConvertibleUrl, isPdfUrl } from '../../media/media';
import { Translate, useI18n } from '../../i18n/core';

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
const probeDocument = async (src: string, signal: AbortSignal, t: Translate): Promise<ProbeState> => {
  try {
    const response = await fetch(src, { headers: { Range: 'bytes=0-0' }, signal });
    const type = (response.headers.get('Content-Type') ?? '').toLowerCase();
    if (!response.ok) {
      const status = `${response.status} ${response.statusText}`.trim();
      return { phase: 'refused', message: t('document.refusedStatus', { status }) };
    }
    if (!type.startsWith('application/pdf')) {
      return { phase: 'refused', message: t('document.refusedType', { type: type.split(';')[0] || t('document.unknownType') }) };
    }
    return { phase: 'pdf' };
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return { phase: 'probing' };
    return { phase: 'refused', message: t('document.fetchFailed') };
  }
};

// Asks the console to convert an e-book, a DjVu scan or an office file to
// a PDF and reads what it says: converted (by which tool, in how long), or
// why not, such as a host with no converter.
const convertDocument = async (streamUrl: string, signal: AbortSignal, t: Translate): Promise<ProbeState> => {
  try {
    const response = await fetch(`${convertUrl(streamUrl)}&info=1`, { signal });
    const status = (await response.json()) as ConvertStatus;
    if (response.ok && status.converted) return { phase: 'converted', status };
    return { phase: 'unconvertible', message: status.error || t('document.convertFailed', { format: status.format?.toUpperCase() || 'file' }) };
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') return { phase: 'converting' };
    return { phase: 'unconvertible', message: t('document.convertUnasked') };
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
  const { t } = useI18n();
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
      void convertDocument(stream.streamUrl, controller.signal, t).then((result) => {
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
    void probeDocument(src, controller.signal, t).then((result) => {
      if (!controller.signal.aborted) setProbe(result);
    });
    return () => controller.abort();
  }, [pdf, convertible, stream.streamUrl, src, isProtected, attempt, t]);

  const toggleFullscreen = () => {
    const root = rootRef.current;
    if (!root) return;
    if (document.fullscreenElement === root) void document.exitFullscreen();
    else void root.requestFullscreen?.();
  };

  const conversionLabel = probe.phase === 'converted'
    ? t('document.converted', { format: ext, converter: probe.status.converter ?? t('document.host') })
      + (probe.status.cached ? t('document.convertedEarlier') : probe.status.seconds ? t('document.convertedIn', { seconds: probe.status.seconds.toFixed(1) }) : '')
    : null;

  return (
    <div ref={rootRef} className={`mp-player mp-player--document ${fullscreen ? 'mp-player--fullscreen' : ''}`} aria-label={t('player.aria.document', { title: stream.title })}>
      <div className="mp-image-toolbar">
        <span className={`mp-badge ${isProtected ? 'mp-badge--protected' : 'mp-badge--direct'}`}>
          {isProtected ? <ShieldCheck size={12} /> : <ShieldOff size={12} />}
          {isProtected ? t('player.protectedRelay') : t('player.direct')}
        </span>
        <span className="mp-badge"><FileText size={12} /> {ext || t('kind.document')}</span>
        {conversionLabel && <span className="mp-badge"><RefreshCw size={12} /> {conversionLabel}</span>}
        <span className="mp-controls-spacer" />
        <button type="button" className="mp-btn" aria-label={fullscreen ? t('player.exitFullscreen') : t('player.fullscreen')} title={t('player.fullscreen')} onClick={toggleFullscreen}>{fullscreen ? <Minimize size={16} /> : <Maximize size={16} />}</button>
      </div>
      {!pdf && !convertible && (
        <div className="mp-overlay mp-overlay--static">
          <FileText size={34} />
          <p>
            {t('document.notRendered', { format: ext || t('document.this') })}
          </p>
          <button type="button" className="btn btn-primary" onClick={() => onOpenInVault(stream.pageUrl ?? stream.streamUrl)}>
            <Globe size={14} aria-hidden="true" /> {t('card.openProtected')}
          </button>
        </div>
      )}
      {probe.phase === 'converting' && (
        <div className="mp-overlay mp-overlay--spinner"><Loader size={30} className="mp-spin" /><span>{t('document.converting', { format: ext })}</span></div>
      )}
      {probe.phase === 'unconvertible' && (
        <div className="mp-overlay mp-overlay--static" role="alert">
          <FileText size={34} />
          <p>{probe.message}</p>
          <button type="button" className="btn btn-primary" onClick={() => onOpenInVault(stream.pageUrl ?? stream.streamUrl)}>
            <Globe size={14} aria-hidden="true" /> {t('card.openProtected')}
          </button>
        </div>
      )}
      {pdf && probe.phase === 'probing' && (
        <div className="mp-overlay mp-overlay--spinner"><Loader size={30} className="mp-spin" /><span>{t('document.checking')}</span></div>
      )}
      {pdf && probe.phase === 'refused' && (
        <div className="mp-overlay mp-overlay--error" role="alert">
          <p>{probe.message}</p>
          <button type="button" className="btn" onClick={onRetryProtected}><RotateCw size={13} aria-hidden="true" /> {t('player.retry')}</button>
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
