import React, { useEffect } from 'react';

interface TrustDataModalProps {
  onClose: () => void;
}

interface Section {
  title: string;
  intro?: string;
  points: string[];
}

// The copy here is a claim about what the software does, so it follows the
// code rather than the ambition: Atlas has no automatic expiry, the console
// has no login, and a playback sample is only the beginning of a file.
const SECTIONS: Section[] = [
  {
    title: 'What this console is',
    intro:
      'ITEMBA NET is an operator console for media discovery and protected browsing. It is not a store, a subscription service or a sign-in gateway, and it does not unlock content that a provider expects you to pay for.',
    points: [],
  },
  {
    title: 'What is kept',
    intro: 'When an Atlas database is configured:',
    points: [
      'Each search writes what it observed: objects and their titles, the host of every listing, the level of evidence reached, the check method and result, the observed country and time, and any sampled fingerprint.',
      'The crawler additionally keeps its frontier of pending searches and page fetches.',
      'Atlas keeps these rows until an operator deletes them. This build has no automatic expiry, and without a configured database nothing is persisted.',
      'Search reports themselves are not kept in server memory between requests.',
    ],
  },
  {
    title: 'What is not kept',
    points: [
      'There are no user accounts and no sign-in profiles. This build has no authentication of its own.',
      'Provider API keys live in local configuration and the process environment. They are not sent to the browser, and connection errors do not include them.',
      'The protected browser is ephemeral. Its profile and any provider session exist only inside the isolated sandbox and end with it, including when you press Destroy.',
    ],
  },
  {
    title: 'What leaves this machine',
    points: [
      'A search sends its query to the public catalogs it uses, such as Apple storefronts, Internet Archive, Wikimedia Commons, NASA+, NFB, PeerTube instances and the other configured drivers. If a web-search provider is configured, one request goes to that provider per search.',
      'Playback loads media either directly from the provider or through the relay, which contacts the provider without forwarding your cookies or referrer.',
      'Country observations are reduced to a country code. Raw addresses and trace data are not stored or returned.',
    ],
  },
  {
    title: 'What the evidence says',
    points: [
      'Listed, reachable, identified, rendered and clean describe separate checks. “Playback started” means a short beginning sample decoded and advanced at normal speed.',
      'None of these levels prove complete playback, licensing, ownership, or availability in another region or at another time. Unknown stays explicitly unknown and unconfirmed access is labelled as such.',
    ],
  },
  {
    title: 'Before exposing the console',
    points: [
      'This build has no login. Keep it on loopback, or put it behind your own authenticated reverse proxy, and treat /api/status, /api/ws and /metrics as operator-only endpoints.',
      'Honour each provider’s terms and your local law. ITEMBA does not bypass payment, sign-in, DRM or regional restrictions.',
    ],
  },
];

export const TrustDataModal: React.FC<TrustDataModalProps> = ({ onClose }) => {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby="trust-data-title"
        onClick={(event) => event.stopPropagation()}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16, marginBottom: 18 }}>
          <div>
            <h2 id="trust-data-title" style={{ fontSize: 18, fontWeight: 800, color: '#f8fafc' }}>
              Data, evidence and trust
            </h2>
            <p style={{ fontSize: 12.5, color: '#94a3b8', marginTop: 2 }}>
              What ITEMBA NET records, what it never sees, and what its evidence does not claim.
            </p>
          </div>
          <button type="button" className="btn" onClick={onClose} aria-label="Close data and trust">
            ✕ Close
          </button>
        </div>

        <div style={{ display: 'grid', gap: 12 }}>
          {SECTIONS.map((section) => (
            <section
              key={section.title}
              style={{ background: '#090d16', border: '1px solid #1e293b', borderRadius: 12, padding: '14px 18px' }}
            >
              <h3 style={{ fontSize: 14, fontWeight: 800, color: '#f1f5f9', marginBottom: 6 }}>{section.title}</h3>
              {section.intro && (
                <p style={{ fontSize: 13, color: '#cbd5e1', lineHeight: 1.6 }}>{section.intro}</p>
              )}
              {section.points.length > 0 && (
                <ul style={{ margin: section.intro ? '8px 0 0 18px' : 0, padding: 0, display: 'grid', gap: 6 }}>
                  {section.points.map((point) => (
                    <li key={point} style={{ fontSize: 13, color: '#cbd5e1', lineHeight: 1.6 }}>
                      {point}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          ))}
        </div>
      </div>
    </div>
  );
};
