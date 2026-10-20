import React, { useEffect, useState } from 'react';
import { Check, Minus, X } from 'lucide-react';
import { FormatEntry } from '../media/formats.generated';

// The format matrix as this console judges it for this host: every row of
// internal/formats, with whether its relay path and its sandbox work are
// available here and why not when not. Read once from /api/formats when the
// panel opens; the rows are the server's, never guessed from the bundle.

interface AvailableFormat extends FormatEntry {
  relayAvailable: boolean;
  sandboxAvailable: boolean;
  because?: string;
}

interface FormatsResponse {
  host: { converters: string[]; hls: boolean; browser: boolean };
  formats: AvailableFormat[];
}

const KIND_TITLES: Record<FormatEntry['kind'], string> = { video: 'Video', audio: 'Audio', image: 'Pictures', document: 'Documents' };
const RELAY_LABELS: Record<string, string> = { player: 'Player', viewer: 'Viewer', reader: 'Reader' };
const PROBE_LABELS: Record<string, string> = { media_probe: 'Media probe', image_decode: 'Header decode', document_parse: 'PDF parse' };
const SANDBOX_LABELS: Record<string, string> = { sample: 'Playback sample', render: 'Render' };
const FINGERPRINT_LABELS: Record<string, string> = { 'video-frames-v1': 'Frame sequence', 'acoustic-bands-v1': 'Acoustic', 'image-dhash-v1': 'Perceptual hash', 'text-simhash-v2': 'Text' };

const Mark: React.FC<{ ok: boolean; label: string; absent?: boolean }> = ({ ok, label, absent }) => {
  if (absent) return <span className="format-mark format-mark--none" title="Not in the matrix"><Minus size={12} aria-hidden="true" /><span className="visually-hidden">none</span></span>;
  return ok
    ? <span className="format-mark format-mark--ok" title={label}><Check size={12} aria-hidden="true" /> {label}</span>
    : <span className="format-mark format-mark--off" title={label}><X size={12} aria-hidden="true" /> {label}</span>;
};

export const FormatMatrix: React.FC = () => {
  const [response, setResponse] = useState<FormatsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void fetch('/api/formats', { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        setResponse((await res.json()) as FormatsResponse);
      })
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setError('The format matrix could not be read from this console.');
      });
    return () => controller.abort();
  }, []);

  if (error) return <p className="scout-error" role="alert">{error}</p>;
  if (!response) return <p className="scout-muted">Reading the format matrix…</p>;
  const { host, formats } = response;
  return (
    <div className="format-matrix">
      <p className="scout-muted">
        This host: {host.converters.length ? `converters ${host.converters.join(', ')}` : 'no document converter'} · HLS library {host.hls ? 'built in' : 'not built'} · sandbox browser {host.browser ? 'present' : 'absent'}.
        A row the code supports but this host cannot take says why. Nothing outside these rows is offered a player, a viewer or a reader.
      </p>
      {(['video', 'audio', 'image', 'document'] as const).map((kind) => (
        <table key={kind} className="format-table">
          <caption>{KIND_TITLES[kind]}</caption>
          <thead>
            <tr><th scope="col">Format</th><th scope="col">Extensions</th><th scope="col">Console</th><th scope="col">Probe</th><th scope="col">Sandbox</th><th scope="col">Fingerprint</th><th scope="col">Note</th></tr>
          </thead>
          <tbody>
            {formats.filter((row) => row.kind === kind).map((row) => (
              <tr key={`${row.kind}-${row.format}`}>
                <th scope="row">{row.label}</th>
                <td><code>{row.extensions.map((ext) => `.${ext}`).join(' ')}</code></td>
                <td>{row.relay ? <Mark ok={row.relayAvailable} label={`${RELAY_LABELS[row.relay] ?? row.relay}${row.converter ? ` via ${row.converter}` : ''}`} /> : <Mark ok={false} label="Protected browser only" />}</td>
                <td>{row.probe ? <Mark ok label={PROBE_LABELS[row.probe] ?? row.probe} /> : <Mark ok={false} label="" absent />}</td>
                <td>{row.sandbox ? <Mark ok={row.sandboxAvailable} label={SANDBOX_LABELS[row.sandbox] ?? row.sandbox} /> : <Mark ok={false} label="" absent />}</td>
                <td>{row.fingerprint ? <Mark ok label={FINGERPRINT_LABELS[row.fingerprint] ?? row.fingerprint} /> : <Mark ok={false} label="" absent />}</td>
                <td className="scout-muted">{[row.note, row.because].filter(Boolean).join(' ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ))}
    </div>
  );
};
