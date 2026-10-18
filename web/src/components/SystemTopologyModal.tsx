import React, { useState } from 'react';

interface SystemTopologyModalProps {
  onClose: () => void;
}

const TOPOLOGY_STEPS = [
  {
    step: 1,
    daemon: 'itemba-scout',
    title: '1. Federated Seed Discovery & Headless CDP Verification',
    boundary: 'Discovery Boundary (Zero User Exposure)',
    color: '#38bdf8',
    detail:
      'Your query is dispatched concurrently to public catalogs (Internet Archive, Open Media) and untrusted web hosts. Before any link is shown to you, a headless Chromium worker visits each candidate page for up to 5s via CDP, sniffing for real <video> nodes or .m3u8/.mpd manifests while pruning fake registration walls.',
  },
  {
    step: 2,
    daemon: 'itemba-shield (Layer 1)',
    title: '2. Layer 1: Pre-Connection DNS Sinkhole (0.0.0.0)',
    boundary: 'Container / Socket Boundary',
    color: '#f59e0b',
    detail:
      'Inside the sandbox, CoreDNS and Chromium host-resolver rules preload OISD, HaGeZi Pro++, and StevenBlack blocklists. Lookups for doubleclick.net, popads.net, exoclick.com, and telemetry domains immediately resolve to 0.0.0.0 before a TCP handshake can occur.',
  },
  {
    step: 3,
    daemon: 'itemba-shield (Layer 2)',
    title: '3. Layer 2: In-Flight CDP Request Interception',
    boundary: 'Network Packet Boundary',
    color: '#a855f7',
    detail:
      'Using Chrome DevTools Protocol Fetch.enable, every outgoing HTTP, XHR, and WebSocket request is inspected against EasyList, EasyPrivacy, and uBlock filter rules. First-party VAST ad tags and tracking pixels are aborted with BlockedByClient while legitimate .m3u8/.mp4 video segments pass through.',
  },
  {
    step: 4,
    daemon: 'itemba-shield (Layer 3)',
    title: '4. Layer 3: DOM Cosmetic Sanitizer & Pop-Up Guillotine',
    boundary: 'DOM Execution Boundary',
    color: '#f43f5e',
    detail:
      'A pre-injected script locks window.open = () => null to prevent tab-under hijacks and attaches a real-time MutationObserver that detects and strips transparent z-index >= 9000 clickjacking overlays sitting above <video> players.',
  },
  {
    step: 5,
    daemon: 'itemba-vault',
    title: '5. Sub-100ms WebRTC Pixel Streaming & Zero-Residue Teardown',
    boundary: 'Client Isolation Airgap',
    color: '#10b981',
    detail:
      'Only sanitized video/viewport frames are encoded and streamed over Pion WebRTC / low-latency socket to your HTML5 <canvas>. Your mouse and keyboard events travel over the WebRTC DataChannel (itemba-input). Clicking Destroy wipes the container and profile in milliseconds.',
  },
];

export const SystemTopologyModal: React.FC<SystemTopologyModalProps> = ({ onClose }) => {
  const [activeStep, setActiveStep] = useState<number>(1);

  const current = TOPOLOGY_STEPS.find((s) => s.step === activeStep) || TOPOLOGY_STEPS[0];

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 18 }}>
          <div>
            <h2 style={{ fontSize: 18, fontWeight: 800, color: '#f8fafc' }}>
              ITEMBA NET — Interactive Isolation Topology & Stream Flow
            </h2>
            <p style={{ fontSize: 12.5, color: '#94a3b8', marginTop: 2 }}>
              Click any stage below to trace how a query and media stream traverse ITEMBA NET&apos;s isolation boundaries.
            </p>
          </div>
          <button className="btn" onClick={onClose}>
            ✕ Close
          </button>
        </div>

        {/* Interactive Pipeline Nodes */}
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(165px, 1fr))',
            gap: 10,
            marginBottom: 20,
          }}
        >
          {TOPOLOGY_STEPS.map((item) => {
            const isSelected = item.step === activeStep;
            return (
              <div
                key={item.step}
                onClick={() => setActiveStep(item.step)}
                style={{
                  background: isSelected ? 'rgba(56, 189, 248, 0.12)' : '#111726',
                  border: `1px solid ${isSelected ? item.color : '#1e293b'}`,
                  borderRadius: 10,
                  padding: 12,
                  cursor: 'pointer',
                  transition: 'all 0.15s',
                }}
              >
                <div
                  style={{
                    fontSize: 10,
                    fontFamily: 'monospace',
                    fontWeight: 800,
                    color: item.color,
                    marginBottom: 4,
                  }}
                >
                  STAGE 0{item.step} • {item.daemon}
                </div>
                <div style={{ fontSize: 12.5, fontWeight: 700, color: '#f1f5f9' }}>{item.title}</div>
              </div>
            );
          })}
        </div>

        {/* Active Stage Inspector */}
        <div
          style={{
            background: '#090d16',
            border: `1px solid ${current.color}`,
            borderRadius: 12,
            padding: 20,
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 }}>
            <span
              className="pill-badge"
              style={{ borderColor: current.color, color: current.color, fontWeight: 700 }}
            >
              Daemon: {current.daemon}
            </span>
            <span className="pill-badge">{current.boundary}</span>
          </div>
          <h3 style={{ fontSize: 17, fontWeight: 800, color: '#f8fafc', marginBottom: 8 }}>{current.title}</h3>
          <p style={{ fontSize: 13.5, color: '#cbd5e1', lineHeight: 1.65 }}>{current.detail}</p>
        </div>
      </div>
    </div>
  );
};
