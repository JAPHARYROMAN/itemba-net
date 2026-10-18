import React, { useState } from 'react';
import { Shield, Radio, Sliders, RefreshCw, CheckCircle2, XCircle } from 'lucide-react';
import { TelemetrySnapshot, ThreatLayer } from '../types';

interface ShieldTelemetryProps {
  shield: TelemetrySnapshot;
  onAddCustomRule: (pattern: string) => void;
  onSyncRemoteList: () => Promise<number | null>;
}

const layerBadgeText = (layer: ThreatLayer) => {
  switch (layer) {
    case 'L1_DNS_SINKHOLE':
      return 'L1 • DNS SINKHOLE';
    case 'L2_CDP_FILTER':
      return 'L3/L2 • CDP FILTER';
    case 'L3_DOM_SWEEPER':
      return 'L3 • DOM SWEEPER';
    case 'L3_POPUP_GUILLOTINE':
      return 'L3 • POP-UP LOCK';
  }
};

export const ShieldTelemetry: React.FC<ShieldTelemetryProps> = ({
  shield,
  onAddCustomRule,
  onSyncRemoteList,
}) => {
  const [activeTab, setActiveTab] = useState<'intercepts' | 'streams' | 'engine'>('intercepts');
  const [layerFilter, setLayerFilter] = useState<'ALL' | 'L1' | 'L2' | 'L3'>('ALL');
  const [customRule, setCustomRule] = useState('');
  const [testUrlInput, setTestUrlInput] = useState('https://ad.doubleclick.net/ddm/vast.xml');
  const [testResult, setTestResult] = useState<{
    blocked: boolean;
    layer?: string;
    rule?: string;
    listSource?: string;
    category?: string;
  } | null>(null);
  const [syncing, setSyncing] = useState(false);
  const [syncMsg, setSyncMsg] = useState<string | null>(null);

  const l3Total = shield.layer3DomCount + shield.layer3PopupCount;

  const filteredEvents = shield.recentEvents.filter((ev) => {
    if (layerFilter === 'ALL') return true;
    if (layerFilter === 'L1') return ev.layer === 'L1_DNS_SINKHOLE';
    if (layerFilter === 'L2') return ev.layer === 'L2_CDP_FILTER';
    if (layerFilter === 'L3') return ev.layer === 'L3_DOM_SWEEPER' || ev.layer === 'L3_POPUP_GUILLOTINE';
    return true;
  });

  const handleTestUrl = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!testUrlInput.trim()) return;
    const res = await fetch('/api/shield/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: testUrlInput.trim(), resourceType: 'Script' }),
    });
    if (res.ok) {
      const data = await res.json();
      setTestResult(data);
    }
  };

  const handleSync = async () => {
    setSyncing(true);
    setSyncMsg(null);
    try {
      const added = await onSyncRemoteList();
      if (added !== null) {
        setSyncMsg(`+${added} live ad/tracker domains compiled into L1 DNS Sinkhole`);
      }
    } finally {
      setSyncing(false);
    }
  };

  return (
    <aside className="sidebar-column">
      <div className="shield-header">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div>
            <div style={{ fontSize: 12.5, fontWeight: 800, letterSpacing: '0.05em', color: '#f8fafc' }}>
              ITEMBA-SHIELD INSPECTOR
            </div>
            <div style={{ fontSize: 11, color: '#94a3b8', marginTop: 2 }}>
              DNS Sinkhole • CDP Fetch Filter • DOM Guillotine
            </div>
          </div>
          <div
            style={{
              background: 'rgba(244, 63, 94, 0.14)',
              border: '1px solid rgba(244, 63, 94, 0.45)',
              color: '#fb7185',
              padding: '4px 10px',
              borderRadius: 8,
              fontFamily: 'monospace',
              fontWeight: 800,
              fontSize: 15,
            }}
            title="Total Neutralized Threats Across All 3 Layers"
          >
            {shield.totalBlocked}
          </div>
        </div>
      </div>

      {/* 3-Layer Counter Boxes (Click to Filter) */}
      <div className="layer-counters">
        <div
          className="layer-stat-box"
          style={{
            cursor: 'pointer',
            borderColor: layerFilter === 'L1' ? '#f59e0b' : undefined,
          }}
          onClick={() => setLayerFilter(layerFilter === 'L1' ? 'ALL' : 'L1')}
        >
          <div style={{ fontSize: 10, color: '#f59e0b', fontWeight: 700 }}>L1 DNS</div>
          <div style={{ fontSize: 17, fontWeight: 800, fontFamily: 'monospace', marginTop: 2 }}>
            {shield.layer1DnsCount}
          </div>
          <div style={{ fontSize: 9.5, color: '#64748b' }}>
            {shield.engineStats?.dnsSinkholeDomains || 80}+ Hosts
          </div>
        </div>

        <div
          className="layer-stat-box"
          style={{
            cursor: 'pointer',
            borderColor: layerFilter === 'L2' ? '#38bdf8' : undefined,
          }}
          onClick={() => setLayerFilter(layerFilter === 'L2' ? 'ALL' : 'L2')}
        >
          <div style={{ fontSize: 10, color: '#38bdf8', fontWeight: 700 }}>L2 CDP</div>
          <div style={{ fontSize: 17, fontWeight: 800, fontFamily: 'monospace', marginTop: 2 }}>
            {shield.layer2CdpCount}
          </div>
          <div style={{ fontSize: 9.5, color: '#64748b' }}>
            {shield.engineStats?.cdpFilterRules || 23} ABP Rules
          </div>
        </div>

        <div
          className="layer-stat-box"
          style={{
            cursor: 'pointer',
            borderColor: layerFilter === 'L3' ? '#f43f5e' : undefined,
          }}
          onClick={() => setLayerFilter(layerFilter === 'L3' ? 'ALL' : 'L3')}
        >
          <div style={{ fontSize: 10, color: '#f43f5e', fontWeight: 700 }}>L3 DOM</div>
          <div style={{ fontSize: 17, fontWeight: 800, fontFamily: 'monospace', marginTop: 2 }}>
            {l3Total}
          </div>
          <div style={{ fontSize: 9.5, color: '#64748b' }}>Z-Index & Pop</div>
        </div>
      </div>

      {/* Inspector Navigation Tabs */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(3, 1fr)',
          gap: 4,
          padding: '8px 12px',
          borderBottom: '1px solid #1e293b',
          background: '#090d16',
        }}
      >
        <button
          type="button"
          className={`btn ${activeTab === 'intercepts' ? 'btn-primary' : ''}`}
          style={{ padding: '5px 6px', fontSize: 11 }}
          onClick={() => setActiveTab('intercepts')}
        >
          <Shield size={12} /> Threats ({filteredEvents.length})
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'streams' ? 'btn-primary' : ''}`}
          style={{ padding: '5px 6px', fontSize: 11 }}
          onClick={() => setActiveTab('streams')}
        >
          <Radio size={12} /> Media ({shield.sniffedStreams?.length || 0})
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'engine' ? 'btn-primary' : ''}`}
          style={{ padding: '5px 6px', fontSize: 11 }}
          onClick={() => setActiveTab('engine')}
        >
          <Sliders size={12} /> Engine
        </button>
      </div>

      {/* TAB 1: Live Intercept Feed */}
      {activeTab === 'intercepts' && (
        <div className="threat-feed">
          {filteredEvents.length === 0 ? (
            <div style={{ color: '#64748b', fontSize: 12, textAlign: 'center', padding: '28px 12px' }}>
              No threats recorded in current filter view. Click{' '}
              <strong>Run 3-Layer Security Benchmark</strong> or isolate an untrusted website in{' '}
              <code>itemba-vault</code> to inspect live CDP/DOM blocks.
            </div>
          ) : (
            filteredEvents.map((ev) => (
              <div key={ev.id} className={`threat-item ${ev.layer}`}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <span
                    style={{
                      fontSize: 10,
                      fontWeight: 800,
                      fontFamily: 'monospace',
                      color:
                        ev.layer === 'L1_DNS_SINKHOLE'
                          ? '#f59e0b'
                          : ev.layer === 'L2_CDP_FILTER'
                          ? '#38bdf8'
                          : '#fb7185',
                    }}
                  >
                    {layerBadgeText(ev.layer)}
                  </span>
                  <span style={{ fontSize: 10, color: '#64748b', fontFamily: 'monospace' }}>
                    {ev.timestamp}
                  </span>
                </div>
                <div style={{ fontWeight: 600, color: '#e2e8f0' }}>{ev.category}</div>
                <div
                  style={{
                    fontFamily: 'monospace',
                    fontSize: 10.5,
                    color: '#94a3b8',
                    wordBreak: 'break-all',
                  }}
                >
                  {ev.target}
                </div>
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    fontSize: 10,
                    color: '#64748b',
                    marginTop: 2,
                  }}
                >
                  <span>Rule: {ev.rule}</span>
                  <span>{ev.listSource}</span>
                </div>
              </div>
            ))
          )}
        </div>
      )}

      {/* TAB 2: Sniffed Media Manifests (.m3u8 / .mp4 / .webm) */}
      {activeTab === 'streams' && (
        <div className="threat-feed">
          {!shield.sniffedStreams || shield.sniffedStreams.length === 0 ? (
            <div style={{ color: '#64748b', fontSize: 12, textAlign: 'center', padding: '28px 12px' }}>
              No media requests observed yet. Media URLs requested by a protected page appear here.
              They can be previews, trailers or ads; full playback is not verified.
            </div>
          ) : (
            shield.sniffedStreams.map((st, idx) => (
              <div
                key={idx}
                className="threat-item"
                style={{ borderLeftColor: '#10b981', gap: 6 }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <span
                    style={{
                      fontSize: 10,
                      fontWeight: 800,
                      fontFamily: 'monospace',
                      color: '#34d399',
                    }}
                  >
                    {st.format} • {st.sourceLayer}
                  </span>
                  <span style={{ fontSize: 10, color: '#64748b', fontFamily: 'monospace' }}>
                    {st.discoveredAt}
                  </span>
                </div>
                <div
                  style={{
                    fontFamily: 'monospace',
                    fontSize: 10.5,
                    color: '#cbd5e1',
                    wordBreak: 'break-all',
                  }}
                >
                  {st.url}
                </div>
                <p style={{ fontSize: 11, color: '#94a3b8', lineHeight: 1.5 }}>
                  Observed media request; full playback unverified. This may be a preview, trailer or ad.
                </p>
              </div>
            ))
          )}
        </div>
      )}

      {/* TAB 3: Rule Engine Compiler & Diagnostic Tester */}
      {activeTab === 'engine' && (
        <div className="threat-feed" style={{ gap: 12 }}>
          {/* Live Rule Tester */}
          <div
            style={{
              background: '#111726',
              border: '1px solid #1e293b',
              borderRadius: 8,
              padding: 12,
            }}
          >
            <div style={{ fontSize: 11.5, fontWeight: 700, color: '#f8fafc', marginBottom: 6 }}>
              Rule Diagnostic Tester
            </div>
            <form onSubmit={handleTestUrl} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <input
                value={testUrlInput}
                onChange={(e) => setTestUrlInput(e.target.value)}
                placeholder="https://ad.doubleclick.net/vast.xml"
                style={{
                  background: '#090d16',
                  border: '1px solid #1e293b',
                  borderRadius: 6,
                  padding: '6px 9px',
                  color: '#f1f5f9',
                  fontSize: 11,
                  fontFamily: 'monospace',
                }}
              />
              <button type="submit" className="btn" style={{ fontSize: 11, padding: '5px 10px' }}>
                Test URL Against 3-Layer Shield
              </button>
            </form>

            {testResult && (
              <div
                style={{
                  marginTop: 8,
                  padding: 8,
                  borderRadius: 6,
                  fontSize: 11,
                  fontFamily: 'monospace',
                  background: testResult.blocked
                    ? 'rgba(244, 63, 94, 0.12)'
                    : 'rgba(16, 185, 129, 0.12)',
                  border: `1px solid ${testResult.blocked ? '#f43f5e' : '#10b981'}`,
                  color: testResult.blocked ? '#fb7185' : '#34d399',
                }}
              >
                {testResult.blocked ? (
                  <div>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 5, fontWeight: 700 }}>
                      <XCircle size={13} /> BLOCKED ({testResult.layer})
                    </div>
                    <div style={{ color: '#e2e8f0', marginTop: 3 }}>Rule: {testResult.rule}</div>
                    <div style={{ color: '#94a3b8' }}>Source: {testResult.listSource}</div>
                  </div>
                ) : (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 5, fontWeight: 700 }}>
                    <CheckCircle2 size={13} /> ALLOWED (Clean Request / Protected Media)
                  </div>
                )}
              </div>
            )}
          </div>

          {/* Custom ABP / Hosts Rule Compiler */}
          <div
            style={{
              background: '#111726',
              border: '1px solid #1e293b',
              borderRadius: 8,
              padding: 12,
            }}
          >
            <div style={{ fontSize: 11.5, fontWeight: 700, color: '#f8fafc', marginBottom: 4 }}>
              Compile Custom Rule (ABP / uBlock / Hosts)
            </div>
            <div style={{ fontSize: 10.5, color: '#94a3b8', marginBottom: 8 }}>
              Supports <code>||domain.com^</code>, <code>@@||whitelist.com^</code>,{' '}
              <code>##.ad-selector</code>, or <code>0.0.0.0 domain</code>.
            </div>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (!customRule.trim()) return;
                onAddCustomRule(customRule.trim());
                setCustomRule('');
              }}
              style={{ display: 'flex', gap: 6 }}
            >
              <input
                value={customRule}
                onChange={(e) => setCustomRule(e.target.value)}
                placeholder="||malvertising-host.net^"
                style={{
                  flex: 1,
                  background: '#090d16',
                  border: '1px solid #1e293b',
                  borderRadius: 6,
                  padding: '6px 9px',
                  color: '#f1f5f9',
                  fontSize: 11,
                  fontFamily: 'monospace',
                }}
              />
              <button type="submit" className="btn btn-primary" style={{ padding: '5px 10px', fontSize: 11 }}>
                Compile
              </button>
            </form>
          </div>

          {/* Live HTTPS Blocklist Sync */}
          <div
            style={{
              background: '#111726',
              border: '1px solid #1e293b',
              borderRadius: 8,
              padding: 12,
            }}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
              <span style={{ fontSize: 11.5, fontWeight: 700, color: '#f8fafc' }}>
                Active Filter Lists
              </span>
              <span style={{ fontSize: 10, color: '#64748b', fontFamily: 'monospace' }}>
                {shield.engineStats?.lastSyncedAt}
              </span>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 10 }}>
              {(shield.engineStats?.activeBlocklists || []).map((list, i) => (
                <div
                  key={i}
                  style={{
                    fontSize: 11,
                    color: '#cbd5e1',
                    display: 'flex',
                    alignItems: 'center',
                    gap: 6,
                  }}
                >
                  <span style={{ color: '#10b981' }}>●</span> {list}
                </div>
              ))}
            </div>
            <button
              type="button"
              className="btn"
              style={{ width: '100%', fontSize: 11 }}
              disabled={syncing}
              onClick={handleSync}
            >
              <RefreshCw size={12} />
              {syncing ? 'Syncing Remote Hosts List...' : 'Sync Live AdServers Blocklist (HTTPS)'}
            </button>
            {syncMsg && (
              <div style={{ marginTop: 6, fontSize: 10.5, color: '#34d399', fontFamily: 'monospace' }}>
                ✓ {syncMsg}
              </div>
            )}
          </div>
        </div>
      )}
    </aside>
  );
};
