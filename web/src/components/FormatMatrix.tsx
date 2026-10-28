import React, { useEffect, useState } from 'react';
import { Check, Minus, X } from 'lucide-react';
import { FormatEntry } from '../media/formats.generated';
import { MessageKey, Translate, useI18n } from '../i18n/core';

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

const KIND_TITLES: Record<FormatEntry['kind'], MessageKey> = { video: 'formats.kind.video', audio: 'formats.kind.audio', image: 'formats.kind.image', document: 'formats.kind.document' };
const RELAY_LABELS: Record<string, MessageKey> = { player: 'formats.relay.player', viewer: 'formats.relay.viewer', reader: 'formats.relay.reader' };
const PROBE_LABELS: Record<string, MessageKey> = { media_probe: 'formats.probe.media_probe', image_decode: 'formats.probe.image_decode', document_parse: 'formats.probe.document_parse' };
const SANDBOX_LABELS: Record<string, MessageKey> = { sample: 'formats.sandbox.sample', render: 'formats.sandbox.render' };
const FINGERPRINT_LABELS: Record<string, MessageKey> = { 'video-frames-v1': 'formats.fingerprint.video-frames-v1', 'acoustic-bands-v1': 'formats.fingerprint.acoustic-bands-v1', 'image-dhash-v1': 'formats.fingerprint.image-dhash-v1', 'text-simhash-v2': 'formats.fingerprint.text-simhash-v2' };

const labelOf = (t: Translate, table: Record<string, MessageKey>, value: string): string => {
  const key = table[value];
  return key ? t(key) : value;
};

const Mark: React.FC<{ ok: boolean; label: string; absent?: boolean }> = ({ ok, label, absent }) => {
  const { t } = useI18n();
  if (absent) return <span className="format-mark format-mark--none" title={t('formats.absent')}><Minus size={12} aria-hidden="true" /><span className="visually-hidden">{t('formats.none')}</span></span>;
  return ok
    ? <span className="format-mark format-mark--ok" title={label}><Check size={12} aria-hidden="true" /> {label}</span>
    : <span className="format-mark format-mark--off" title={label}><X size={12} aria-hidden="true" /> {label}</span>;
};

export const FormatMatrix: React.FC = () => {
  const { t } = useI18n();
  const [response, setResponse] = useState<FormatsResponse | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    void fetch('/api/formats', { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        setResponse((await res.json()) as FormatsResponse);
      })
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setFailed(true);
      });
    return () => controller.abort();
  }, []);

  if (failed) return <p className="scout-error" role="alert">{t('formats.error')}</p>;
  if (!response) return <p className="scout-muted">{t('formats.loading')}</p>;
  const { host, formats } = response;
  return (
    <div className="format-matrix">
      <p className="scout-muted">
        {t('formats.host', {
          converters: host.converters.length ? t('formats.host.converters', { list: host.converters.join(', ') }) : t('formats.host.noConverters'),
          hls: host.hls ? t('formats.host.hlsBuilt') : t('formats.host.hlsMissing'),
          browser: host.browser ? t('formats.host.browserPresent') : t('formats.host.browserAbsent'),
        })}{' '}
        {t('formats.host.note')}
      </p>
      {(['video', 'audio', 'image', 'document'] as const).map((kind) => (
        <table key={kind} className="format-table">
          <caption>{t(KIND_TITLES[kind])}</caption>
          <thead>
            <tr><th scope="col">{t('formats.col.format')}</th><th scope="col">{t('formats.col.extensions')}</th><th scope="col">{t('formats.col.console')}</th><th scope="col">{t('formats.col.probe')}</th><th scope="col">{t('formats.col.sandbox')}</th><th scope="col">{t('formats.col.fingerprint')}</th><th scope="col">{t('formats.col.note')}</th></tr>
          </thead>
          <tbody>
            {formats.filter((row) => row.kind === kind).map((row) => (
              <tr key={`${row.kind}-${row.format}`}>
                <th scope="row">{row.label}</th>
                <td><code>{row.extensions.map((ext) => `.${ext}`).join(' ')}</code></td>
                <td>{row.relay ? <Mark ok={row.relayAvailable} label={`${labelOf(t, RELAY_LABELS, row.relay)}${row.converter ? t('formats.via', { converter: row.converter }) : ''}`} /> : <Mark ok={false} label={t('formats.browserOnly')} />}</td>
                <td>{row.probe ? <Mark ok label={labelOf(t, PROBE_LABELS, row.probe)} /> : <Mark ok={false} label="" absent />}</td>
                <td>{row.sandbox ? <Mark ok={row.sandboxAvailable} label={labelOf(t, SANDBOX_LABELS, row.sandbox)} /> : <Mark ok={false} label="" absent />}</td>
                <td>{row.fingerprint ? <Mark ok label={labelOf(t, FINGERPRINT_LABELS, row.fingerprint)} /> : <Mark ok={false} label="" absent />}</td>
                <td className="scout-muted">{[row.note, row.because].filter(Boolean).join(' ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ))}
    </div>
  );
};
