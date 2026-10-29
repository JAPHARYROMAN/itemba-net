import React, { lazy, Suspense, useEffect, useState } from 'react';
import { ArrowLeft, ArrowRight, Globe, MonitorPlay, Play, RotateCw, ShieldCheck, Sparkles, Trash2, Tv } from 'lucide-react';
import { ActiveRelayStream, SniffedStream, VaultStatus } from '../types';
import { streamFromPastedUrl } from '../media/media';
import { Translate, useI18n } from '../i18n/core';
import { RbiViewer } from './RbiViewer';
import { ChunkBoundary, ChunkError } from './ChunkBoundary';

// The player pulls in hls.js and the media viewers; it loads when the
// operator switches to the media stage, not with the console shell.
const MediaStage = lazy(() => import('./player/MediaStage').then(({ MediaStage }) => ({ default: MediaStage })));

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

const hostnameOf = (raw: string | undefined): string => {
  if (!raw) return '';
  try {
    return new URL(raw).hostname;
  } catch {
    return '';
  }
};

// A media request the sandbox observed, offered to the player as it was
// seen: it may be the film, a trailer or an advertisement, and the badge on
// the card says so until the player has actually decoded it.
const observedStream = (vault: VaultStatus, observed: SniffedStream, t: Translate): ActiveRelayStream => ({
  title: vault.pageTitle || hostnameOf(vault.currentUrl) || t('vault.observed.defaultTitle'),
  streamUrl: observed.url,
  pageUrl: vault.currentUrl,
  provider: t('vault.observed.provider', { host: hostnameOf(vault.currentUrl) || t('vault.observed.sandbox') }),
  format: observed.format,
  resolution: observed.resolution,
  useZeroTrustProxy: true,
});

// The Vault card: the mode tabs and address bar on top, and underneath either
// the protected media stage or the remote browser viewport.
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
  const { t } = useI18n();
  const [urlInput, setUrlInput] = useState(vault.currentUrl || '');

  useEffect(() => {
    if (vault.currentUrl) setUrlInput(vault.currentUrl);
  }, [vault.currentUrl]);

  const observed = vault.extractedStreams ?? [];

  return (
    <div className={`vault-card ${vault.active || activeRelay ? 'active-isolation' : ''}`}>
      <div className="vault-toolbar">
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          <button
            type="button"
            className={`btn ${viewportMode === 'relay' ? 'btn-primary' : ''}`}
            style={{ padding: '6px 11px', fontSize: 11.5 }}
            onClick={() => setViewportMode('relay')}
            aria-pressed={viewportMode === 'relay'}
            title={t('vault.player.title')}
          >
            <MonitorPlay size={13} /> {t('vault.player')} {activeRelay ? '●' : ''}
          </button>
          <button
            type="button"
            className={`btn ${viewportMode === 'rbi' ? 'btn-primary' : ''}`}
            style={{ padding: '6px 11px', fontSize: 11.5 }}
            onClick={() => setViewportMode('rbi')}
            aria-pressed={viewportMode === 'rbi'}
            title={t('vault.browser.title')}
          >
            <Globe size={13} /> {t('vault.browser')} {vault.active ? '●' : ''}
          </button>
          {viewportMode === 'rbi' && vault.adBlockingEnabled && (
            <span className="pill-badge live" role="status"><ShieldCheck size={12} /> {t('vault.adBlocking')}</span>
          )}
          {viewportMode === 'rbi' && (
            <div style={{ display: 'flex', gap: 4, marginInlineStart: 4 }}>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('back')} title={t('vault.back')} aria-label={t('vault.back')}>
                <ArrowLeft size={13} />
              </button>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('forward')} title={t('vault.forward')} aria-label={t('vault.forward')}>
                <ArrowRight size={13} />
              </button>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('reload')} title={t('vault.reload.title')} aria-label={t('vault.reload')}>
                <RotateCw size={13} />
              </button>
            </div>
          )}
        </div>

        <form
          className="vault-address-bar"
          onSubmit={(event) => {
            event.preventDefault();
            const trimmed = urlInput.trim();
            if (!trimmed) return;
            const pasted = streamFromPastedUrl(trimmed, t);
            if (pasted) onPlayRelayStream(pasted);
            else onLaunchUrl(trimmed, false);
          }}
        >
          <ShieldCheck size={14} color="#10b981" />
          <input
            className="vault-address-input"
            value={urlInput}
            onChange={(event) => setUrlInput(event.target.value)}
            aria-label={t('vault.address')}
            placeholder={t('vault.address.placeholder')}
          />
          <button type="submit" className="btn" style={{ padding: '3px 10px', fontSize: 11 }}>
            {t('vault.open')}
          </button>
        </form>

        {viewportMode === 'rbi' && (
          <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <button
              type="button"
              className="btn btn-theater"
              disabled={!vault.active}
              onClick={onActivateTheater}
              title={t('vault.theater.title')}
            >
              <Tv size={13} /> {t('vault.theater')}
            </button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={!vault.active}
              onClick={onDestroyVault}
              title={t('vault.destroy.title')}
            >
              <Trash2 size={13} /> {t('vault.destroy')}
            </button>
          </div>
        )}
      </div>

      {observed.length > 0 && (
        <div className="vault-observed">
          <div className="vault-observed-head">
            <Sparkles size={14} color="#10b981" />
            <strong>{t(observed.length === 1 ? 'vault.observed.head.one' : 'vault.observed.head.other', { count: observed.length })}</strong>
            <span className="scout-muted">{t('vault.observed.note')}</span>
          </div>
          <ul className="vault-observed-list">
            {observed.slice(0, 3).map((item) => (
              <li key={item.url}>
                <span className="vault-observed-url">[{item.format}] {item.url}</span>
                <button type="button" className="btn" style={{ padding: '4px 9px', fontSize: 11 }} onClick={() => onPlayRelayStream(observedStream(vault, item, t))}>
                  <Play size={12} aria-hidden="true" /> {t('vault.observed.try')}
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {viewportMode === 'relay' ? (
        <ChunkBoundary
          fallback={
            <ChunkError
              className="chunk-error--stage"
              title={t('app.chunk.title')}
              body={t('app.chunk.body')}
              action={t('app.chunk.reload')}
              onReload={() => window.location.reload()}
            />
          }
        >
          <Suspense fallback={<div className="media-stage media-stage--empty" aria-busy="true" />}>
            <MediaStage stream={activeRelay} onSelectStream={onPlayRelayStream} onOpenInVault={(url) => onLaunchUrl(url, false)} />
          </Suspense>
        </ChunkBoundary>
      ) : (
        <div className={vault.active ? 'viewport-stage' : 'viewport-stage viewport-stage--empty'}>
          {vault.active ? (
            <RbiViewer vault={vault} latestFrame={latestFrame} sendWsInput={sendWsInput} />
          ) : (
            <div style={{ textAlign: 'center', padding: 36, maxWidth: 620 }}>
              <Globe size={36} color="#38bdf8" style={{ marginBottom: 12 }} />
              <h3 style={{ fontSize: 18, fontWeight: 700, marginBottom: 8, color: '#f8fafc' }}>
                {t('vault.empty.title')}
              </h3>
              <p style={{ fontSize: 13, color: '#94a3b8', lineHeight: 1.6, marginBottom: 18 }}>
                {t('vault.empty.lead')} <strong>{t('vault.empty.strong')}</strong> {t('vault.empty.rest')}
              </p>
              <div style={{ display: 'flex', gap: 10, justifyContent: 'center', flexWrap: 'wrap' }}>
                <button className="btn btn-primary" onClick={() => onLaunchUrl('https://archive.org/embed/night_of_the_living_dead', false)}>
                  <Globe size={14} /> {t('vault.empty.archive')}
                </button>
                <button
                  className="btn btn-theater"
                  onClick={() => onLaunchUrl(`${window.location.protocol}//${window.location.hostname}:8080/testbench/sketchy-cinema?q=Adversarial+Sandbox+Test`, false)}
                >
                  <ShieldCheck size={14} /> {t('vault.empty.testbench')}
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
