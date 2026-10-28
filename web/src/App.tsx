import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Shield, ShieldCheck, Cpu, Network, Wifi, WifiOff, Loader } from 'lucide-react';
import { ActiveRelayStream, SearchKind, SearchKnownEvent, SearchMergedEvent, SearchReport, TelemetrySnapshot, VaultStatus } from './types';
import { readSearchStream, searchStreamUrl } from './searchStream';
import { VaultViewport } from './components/VaultViewport';
import './ladder.css';
import { SearchScoutPanel } from './components/SearchScoutPanel';
import { ShieldTelemetry } from './components/ShieldTelemetry';
import { SystemTopologyModal } from './components/SystemTopologyModal';
import { TrustDataModal } from './components/TrustDataModal';

// Backoff for reopening the console socket: it starts at the minimum, doubles
// after each failed attempt up to the cap, and resets once a connection opens.
const WS_RECONNECT_MIN_MS = 1000;
const WS_RECONNECT_MAX_MS = 15000;

// What the navbar badge shows about the console socket. 'connecting' covers
// page load until the first open; 'reconnecting' carries the backoff delay
// while a retry is scheduled and drops it once that retry is in flight.
type WsConnection =
  | { phase: 'connecting' }
  | { phase: 'live' }
  | { phase: 'reconnecting'; retryInMs?: number };

const describeWsConnection = (conn: WsConnection): { label: string; title: string } => {
  switch (conn.phase) {
    case 'live':
      return { label: 'Live', title: 'Console WebSocket open: frames and shield events are streaming.' };
    case 'reconnecting': {
      if (conn.retryInMs === undefined) {
        return { label: 'Reconnecting…', title: 'Console WebSocket closed; a reconnect attempt is in flight.' };
      }
      const secs = `${Math.round(conn.retryInMs / 1000)}s`;
      return { label: `Reconnecting in ${secs}`, title: `Console WebSocket closed; retrying in ${secs}.` };
    }
    default:
      return { label: 'Connecting', title: 'Opening the console WebSocket (/api/ws).' };
  }
};

export const App: React.FC = () => {
  const [vault, setVault] = useState<VaultStatus>({
    active: false,
    theaterActive: false,
    webrtcState: 'disconnected',
    framesStreamed: 0,
    uptimeSeconds: 0,
    extractedStreams: [],
  });

  const [shield, setShield] = useState<TelemetrySnapshot>({
    totalBlocked: 0,
    totalAllowed: 0,
    layer1DnsCount: 0,
    layer2CdpCount: 0,
    layer3DomCount: 0,
    layer3PopupCount: 0,
    engineStats: {
      dnsSinkholeDomains: 82,
      cdpFilterRules: 23,
      domCosmeticRules: 13,
      exceptionRules: 0,
      activeBlocklists: [
        'OISD Big (DNS Sinkhole)',
        'HaGeZi Multi Pro++',
        'StevenBlack Unified Hosts',
        'EasyList & EasyList Video (VAST/IMA)',
        'EasyPrivacy Telemetry & Replay',
        'uBlock Origin Badware & Annoyances',
      ],
      lastSyncedAt: 'Active',
    },
    sniffedStreams: [],
    recentEvents: [],
  });

  const [viewportMode, setViewportMode] = useState<'relay' | 'rbi'>('rbi');
  const [activeRelay, setActiveRelay] = useState<ActiveRelayStream | null>(null);

  const [report, setReport] = useState<SearchReport | null>(null);
  const [known, setKnown] = useState<SearchKnownEvent | null>(null);
  const [merged, setMerged] = useState<SearchMergedEvent | null>(null);
  const [searching, setSearching] = useState<boolean>(false);
  const [searchError, setSearchError] = useState<string | null>(null);
  const [launchError, setLaunchError] = useState<string | null>(null);
  const [launchingUrl, setLaunchingUrl] = useState<string | null>(null);
  const [preferDocker, setPreferDocker] = useState<boolean>(false);
  const [showTopology, setShowTopology] = useState<boolean>(false);
  const [showTrust, setShowTrust] = useState<boolean>(false);
  const [latestFrame, setLatestFrame] = useState<{ data: string; ts: number; url?: string } | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const [wsConnection, setWsConnection] = useState<WsConnection>({ phase: 'connecting' });
  const searchAbortRef = useRef<AbortController | null>(null);
  const searchSequenceRef = useRef(0);
  const viewportRef = useRef<HTMLDivElement | null>(null);

  const fetchStatus = useCallback(async () => {
    try {
      const res = await fetch('/api/status');
      if (res.ok) {
        const data = await res.json();
        if (data.vault) setVault(data.vault);
        if (data.shield) setShield(data.shield);
      }
    } catch {
      // ignore transient network errors
    }
  }, []);

  useEffect(() => {
    fetchStatus();
    const interval = setInterval(fetchStatus, 2200);

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${proto}//${window.location.host}/api/ws`;

    // The console drops a peer that goes quiet and every peer when it restarts,
    // so the socket is reopened after a backoff. The reconnect state lives in
    // this closure so the cleanup below can stop it completely.
    let disposed = false;
    let backoffMs = WS_RECONNECT_MIN_MS;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    // Returns the delay it scheduled so the close handler can show it.
    const scheduleReconnect = (): number | null => {
      if (disposed || reconnectTimer !== null) return null;
      const delay = backoffMs;
      backoffMs = Math.min(backoffMs * 2, WS_RECONNECT_MAX_MS);
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        connect();
      }, delay);
      return delay;
    };

    const connect = () => {
      if (disposed) return;
      // A scheduled retry is now in flight: stay on 'reconnecting' but drop the
      // countdown it was showing. The first connect leaves 'connecting' as is.
      setWsConnection((prev) =>
        prev.phase === 'reconnecting' && prev.retryInMs !== undefined ? { phase: 'reconnecting' } : prev,
      );
      const ws = new WebSocket(wsUrl);
      wsRef.current = ws;

      ws.onopen = () => {
        backoffMs = WS_RECONNECT_MIN_MS;
        setWsConnection({ phase: 'live' });
      };

      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data);
          if (msg.type === 'frame' && msg.data) {
            setLatestFrame({ data: msg.data, ts: msg.ts || Date.now(), url: msg.url });
          } else if (msg.type === 'threat' && msg.shield) {
            setShield(msg.shield);
          }
        } catch {
          // ignore malformed packets
        }
      };

      ws.onerror = () => {
        // A failed connection attempt is always followed by close; closing an
        // open socket here sends a mid-stream error down the same path.
        ws.close();
      };

      ws.onclose = () => {
        if (wsRef.current === ws) wsRef.current = null;
        const retryInMs = scheduleReconnect();
        // After cleanup the close belongs to a socket this effect no longer owns
        // (StrictMode's first mount), so it must not touch the badge.
        if (!disposed) setWsConnection({ phase: 'reconnecting', retryInMs: retryInMs ?? undefined });
      };
    };

    connect();

    return () => {
      disposed = true;
      clearInterval(interval);
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      const ws = wsRef.current;
      if (ws) {
        wsRef.current = null;
        ws.close();
      }
      searchAbortRef.current?.abort();
    };
  }, [fetchStatus]);

  const sendWsInput = useCallback((ev: Record<string, unknown>) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(ev));
    }
  }, []);

  const handleSearch = useCallback(
    async (query: string, region = 'ALL', fullLengthOnly = true, kind: SearchKind = 'video') => {
      if (!query.trim()) return;
      searchAbortRef.current?.abort();
      const controller = new AbortController();
      searchAbortRef.current = controller;
      const sequence = ++searchSequenceRef.current;
      // A newer search, or an abort, makes every later event of this one stale.
      const current = () => sequence === searchSequenceRef.current && !controller.signal.aborted;
      setSearching(true);
      setSearchError(null);
      setKnown(null);
      setMerged(null);
      try {
        // The stream answers from the index first, then runs fresh discovery,
        // then sends the ranked hundred across both.
        await readSearchStream(searchStreamUrl(query.trim(), region, fullLengthOnly, kind), controller.signal, {
          onKnown: (event) => { if (current()) setKnown(event); },
          onReport: (data) => { if (current()) setReport(data); },
          onMerged: (event) => { if (current()) setMerged(event); },
        });
      } catch (error) {
        if (current()) {
          setSearchError(error instanceof Error ? error.message : 'Could not run the search. Check the server connection and try again.');
        }
      } finally {
        if (current()) {
          setSearching(false);
          void fetchStatus();
        }
      }
    },
    [fetchStatus]
  );

  const handleRunBenchmark = async () => {
    searchAbortRef.current?.abort();
    const sequence = ++searchSequenceRef.current;
    setSearching(true);
    setSearchError(null);
    setKnown(null);
    setMerged(null);
    try {
      const res = await fetch('/api/shield/benchmark', { method: 'POST' });
      if (!res.ok) throw new Error(`The ad-blocking benchmark failed (${res.status}).`);
      const data = await res.json();
      if (sequence === searchSequenceRef.current) {
        if (data.report) setReport(data.report);
        if (data.shield) setShield(data.shield);
      }
    } catch (error) {
      if (sequence === searchSequenceRef.current) setSearchError(error instanceof Error ? error.message : 'Could not run the ad-blocking benchmark.');
    } finally {
      if (sequence === searchSequenceRef.current) setSearching(false);
    }
  };

  const handleLaunchRbi = async (url: string, theaterMode: boolean) => {
    if (launchingUrl) return;
    setLaunchingUrl(url);
    setLaunchError(null);
    try {
      const res = await fetch('/api/vault/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url, theaterMode, preferDocker }),
      });
      if (!res.ok) {
        const detail = await res.json().catch(() => null);
        throw new Error(detail?.error || `The protected browser could not open this site (${res.status}). Try again.`);
      }
      const data = await res.json();
      if (!data.vault?.active) throw new Error('The protected browser did not start. Try opening the site again.');
      if (data.vault.adBlockingEnabled !== true) throw new Error('The browser could not confirm that ad blocking is enabled. Try opening the site again.');
      if (data.vault) setVault(data.vault);
      if (data.shield) setShield(data.shield);
      setLatestFrame(null);
      setViewportMode('rbi');
      viewportRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (error) {
      setLaunchError(error instanceof Error ? error.message : 'Could not open the site in the protected browser. Check the server connection and try again.');
    } finally {
      setLaunchingUrl(null);
    }
  };

  const handleVaultControl = async (action: 'back' | 'forward' | 'reload') => {
    const res = await fetch('/api/vault/control', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action }),
    });
    if (res.ok) {
      const data = await res.json();
      if (data.vault) setVault(data.vault);
    }
  };

  const handleActivateTheater = async () => {
    const res = await fetch('/api/vault/theater', { method: 'POST' });
    if (res.ok) {
      const data = await res.json();
      if (data.vault) setVault(data.vault);
    }
  };

  const handleDestroyVault = async () => {
    const res = await fetch('/api/vault/destroy', { method: 'POST' });
    if (res.ok) {
      const data = await res.json();
      if (data.vault) setVault(data.vault);
      setLatestFrame(null);
    }
  };

  const handlePlayRelayStream = (stream: ActiveRelayStream) => {
    setActiveRelay(stream);
    setViewportMode('relay');
    window.scrollTo({ top: 0, behavior: 'smooth' });
  };

  const handleAddCustomRule = async (pattern: string) => {
    const res = await fetch('/api/shield/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pattern }),
    });
    if (res.ok) {
      const snap = await res.json();
      setShield(snap);
    }
  };

  const handleSyncRemoteList = async (): Promise<number | null> => {
    const res = await fetch('/api/shield/sync', { method: 'POST' });
    if (res.ok) {
      const data = await res.json();
      if (data.shield) setShield(data.shield);
      return typeof data.addedDomains === 'number' ? data.addedDomains : 0;
    }
    return null;
  };

  const wsBadge = describeWsConnection(wsConnection);
  const wsBadgeTone = { connecting: '', live: 'live', reconnecting: 'amber' }[wsConnection.phase];

  return (
    <div className="app-shell">
      <header className="top-navbar">
        <div className="brand-group">
          <div className="brand-logo">IN</div>
          <div>
            <div className="brand-title">ITEMBA NET</div>
            <div className="brand-subtitle">
              itemba-scout (CDP Verifier) • itemba-vault (WebRTC RBI & Stream Relay) • itemba-shield (3-Layer Ad Engine)
            </div>
          </div>
        </div>

        <div className="nav-badges">
          <button
            type="button"
            className={`pill-badge ${preferDocker ? 'cyan' : 'live'}`}
            style={{ cursor: 'pointer' }}
            onClick={() => setPreferDocker(!preferDocker)}
            title="Toggle Vault Sandbox Runtime (Hybrid Local Isolated Chromium vs Docker Linux Container)"
          >
            <Cpu size={12} />
            Runtime: {preferDocker ? 'Docker Container (Local Fallback)' : 'Isolated Chromium Engine'}
          </button>

          <span className="pill-badge cyan">
            <Shield size={12} /> Threats Blocked: <strong>{shield.totalBlocked}</strong>
          </span>

          <span
            className={`pill-badge ws-badge ws-${wsConnection.phase} ${wsBadgeTone}`}
            role="status"
            aria-live="polite"
            title={wsBadge.title}
          >
            {wsConnection.phase === 'live' ? (
              <Wifi size={12} />
            ) : wsConnection.phase === 'reconnecting' ? (
              <WifiOff size={12} />
            ) : (
              <Loader size={12} />
            )}
            Console: <strong>{wsBadge.label}</strong>
          </span>

          <button type="button" className="btn" onClick={() => setShowTopology(true)}>
            <Network size={13} /> Architecture Topology
          </button>

          <button
            type="button"
            className="btn"
            onClick={() => setShowTrust(true)}
            title="What ITEMBA NET records, sends and claims about the media it finds"
          >
            <ShieldCheck size={13} /> Data &amp; trust
          </button>
        </div>
      </header>

      <div className="workspace-grid">
        <main className="main-column">
          <div ref={viewportRef} className="movie-viewport-anchor">
            <VaultViewport
              vault={vault}
              activeRelay={activeRelay}
              viewportMode={viewportMode}
              setViewportMode={setViewportMode}
              latestFrame={latestFrame}
              sendWsInput={sendWsInput}
              onLaunchUrl={handleLaunchRbi}
              onVaultControl={handleVaultControl}
              onActivateTheater={handleActivateTheater}
              onDestroyVault={handleDestroyVault}
              onPlayRelayStream={handlePlayRelayStream}
            />
          </div>

          <SearchScoutPanel
            report={report}
            known={known}
            merged={merged}
            searching={searching}
            searchError={searchError}
            launchError={launchError}
            launchingUrl={launchingUrl}
            onSearch={handleSearch}
            onRunBenchmark={handleRunBenchmark}
            onLaunchRbi={handleLaunchRbi}
            onPlayRelay={handlePlayRelayStream}
          />
        </main>

        <ShieldTelemetry
          shield={shield}
          onAddCustomRule={handleAddCustomRule}
          onSyncRemoteList={handleSyncRemoteList}
        />
      </div>

      {showTopology && <SystemTopologyModal onClose={() => setShowTopology(false)} />}
      {showTrust && <TrustDataModal onClose={() => setShowTrust(false)} />}
    </div>
  );
};
