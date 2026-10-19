import React, { useEffect, useState } from 'react';
import { ArrowLeft, ArrowRight, Globe, MonitorPlay, Play, RotateCw, ShieldCheck, Sparkles, Trash2, Tv } from 'lucide-react';
import { ActiveRelayStream, SniffedStream, VaultStatus } from '../types';
import { streamFromPastedUrl } from '../media/media';
import { MediaStage } from './player/MediaStage';
import { RbiViewer } from './RbiViewer';

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
const observedStream = (vault: VaultStatus, observed: SniffedStream): ActiveRelayStream => ({
  title: vault.pageTitle || hostnameOf(vault.currentUrl) || 'Observed media request',
  streamUrl: observed.url,
  pageUrl: vault.currentUrl,
  provider: `${hostnameOf(vault.currentUrl) || 'sandbox'} · observed request`,
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
            title="Video, audio, images and documents through the Shield-filtered relay"
          >
            <MonitorPlay size={13} /> Media player {activeRelay ? '●' : ''}
          </button>
          <button
            type="button"
            className={`btn ${viewportMode === 'rbi' ? 'btn-primary' : ''}`}
            style={{ padding: '6px 11px', fontSize: 11.5 }}
            onClick={() => setViewportMode('rbi')}
            aria-pressed={viewportMode === 'rbi'}
            title="Interactive remote browser isolation (Chromium + CDP + Pion WebRTC)"
          >
            <Globe size={13} /> Protected browser {vault.active ? '●' : ''}
          </button>
          {viewportMode === 'rbi' && vault.adBlockingEnabled && (
            <span className="pill-badge live" role="status"><ShieldCheck size={12} /> Ad blocking enabled</span>
          )}
          {viewportMode === 'rbi' && (
            <div style={{ display: 'flex', gap: 4, marginLeft: 4 }}>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('back')} title="Back" aria-label="Back">
                <ArrowLeft size={13} />
              </button>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('forward')} title="Forward" aria-label="Forward">
                <ArrowRight size={13} />
              </button>
              <button type="button" className="btn" style={{ padding: '6px 8px' }} disabled={!vault.active} onClick={() => onVaultControl('reload')} title="Reload sandbox page" aria-label="Reload">
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
            const pasted = streamFromPastedUrl(trimmed);
            if (pasted) onPlayRelayStream(pasted);
            else onLaunchUrl(trimmed, false);
          }}
        >
          <ShieldCheck size={14} color="#10b981" />
          <input
            className="vault-address-input"
            value={urlInput}
            onChange={(event) => setUrlInput(event.target.value)}
            aria-label="Address"
            placeholder="Paste a page URL for the protected browser, or a media URL (.m3u8, .mp4, .mp3, .jpg, .pdf …) for the player"
          />
          <button type="submit" className="btn" style={{ padding: '3px 10px', fontSize: 11 }}>
            Open
          </button>
        </form>

        {viewportMode === 'rbi' && (
          <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
            <button
              type="button"
              className="btn btn-theater"
              disabled={!vault.active}
              onClick={onActivateTheater}
              title="Promote the <video> element inside the remote Chromium sandbox to full-viewport cinema mode"
            >
              <Tv size={13} /> Isolate DOM &lt;video&gt;
            </button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={!vault.active}
              onClick={onDestroyVault}
              title="Destroy the isolated container/process and wipe its ephemeral profile"
            >
              <Trash2 size={13} /> Destroy Vault
            </button>
          </div>
        )}
      </div>

      {observed.length > 0 && (
        <div className="vault-observed">
          <div className="vault-observed-head">
            <Sparkles size={14} color="#10b981" />
            <strong>{observed.length} observed media request{observed.length === 1 ? '' : 's'} · full playback unverified</strong>
            <span className="scout-muted">Observed requests can be trailers, previews or advertisements. Trying one plays it through the protected relay.</span>
          </div>
          <ul className="vault-observed-list">
            {observed.slice(0, 3).map((item) => (
              <li key={item.url}>
                <span className="vault-observed-url">[{item.format}] {item.url}</span>
                <button type="button" className="btn" style={{ padding: '4px 9px', fontSize: 11 }} onClick={() => onPlayRelayStream(observedStream(vault, item))}>
                  <Play size={12} aria-hidden="true" /> Try in player
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {viewportMode === 'relay' ? (
        <MediaStage stream={activeRelay} onSelectStream={onPlayRelayStream} onOpenInVault={(url) => onLaunchUrl(url, false)} />
      ) : (
        <div className="viewport-stage">
          {vault.active ? (
            <RbiViewer vault={vault} latestFrame={latestFrame} sendWsInput={sendWsInput} />
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
                <button className="btn btn-primary" onClick={() => onLaunchUrl('https://archive.org/embed/night_of_the_living_dead', false)}>
                  <Globe size={14} /> Isolate Internet Archive Player in RBI Vault
                </button>
                <button
                  className="btn btn-theater"
                  onClick={() => onLaunchUrl(`${window.location.protocol}//${window.location.hostname}:8080/testbench/sketchy-cinema?q=Adversarial+Sandbox+Test`, false)}
                >
                  <ShieldCheck size={14} /> Launch Adversarial Ad-Trap Testbench in Vault
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
