import React from 'react';
import { MessageKey, useI18n } from '../i18n/core';
import { useDialog } from './useDialog';

interface TrustDataModalProps {
  onClose: () => void;
}

interface Section {
  title: MessageKey;
  intro?: MessageKey;
  points: MessageKey[];
}

// The copy here is a claim about what the software does, so it follows the
// code rather than the ambition: Atlas has no automatic expiry, the console
// has no login, and a playback sample is only the beginning of a file.
const SECTIONS: Section[] = [
  { title: 'trust.s1.title', intro: 'trust.s1.intro', points: [] },
  { title: 'trust.s2.title', intro: 'trust.s2.intro', points: ['trust.s2.p1', 'trust.s2.p2', 'trust.s2.p3', 'trust.s2.p4'] },
  { title: 'trust.s3.title', points: ['trust.s3.p1', 'trust.s3.p2', 'trust.s3.p3'] },
  { title: 'trust.s4.title', points: ['trust.s4.p1', 'trust.s4.p2', 'trust.s4.p3'] },
  { title: 'trust.s5.title', points: ['trust.s5.p1', 'trust.s5.p2'] },
  { title: 'trust.s6.title', points: ['trust.s6.p1', 'trust.s6.p2'] },
];

export const TrustDataModal: React.FC<TrustDataModalProps> = ({ onClose }) => {
  const { t } = useI18n();
  const dialogRef = useDialog<HTMLDivElement>(onClose);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        ref={dialogRef}
        className="modal-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby="trust-data-title"
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16, marginBottom: 18 }}>
          <div>
            <h2 id="trust-data-title" style={{ fontSize: 18, fontWeight: 800, color: '#f8fafc' }}>
              {t('trust.title')}
            </h2>
            <p style={{ fontSize: 12.5, color: '#94a3b8', marginTop: 2 }}>
              {t('trust.subtitle')}
            </p>
          </div>
          <button type="button" className="btn" onClick={onClose} aria-label={t('trust.close')}>
            ✕ {t('app.close')}
          </button>
        </div>

        <div style={{ display: 'grid', gap: 12 }}>
          {SECTIONS.map((section) => (
            <section
              key={section.title}
              style={{ background: '#090d16', border: '1px solid #1e293b', borderRadius: 12, padding: '14px 18px' }}
            >
              <h3 style={{ fontSize: 14, fontWeight: 800, color: '#f1f5f9', marginBottom: 6 }}>{t(section.title)}</h3>
              {section.intro && (
                <p style={{ fontSize: 13, color: '#cbd5e1', lineHeight: 1.6 }}>{t(section.intro)}</p>
              )}
              {section.points.length > 0 && (
                <ul style={{ margin: 0, marginTop: section.intro ? 8 : 0, paddingInlineStart: 18, display: 'grid', gap: 6 }}>
                  {section.points.map((point) => (
                    <li key={point} style={{ fontSize: 13, color: '#cbd5e1', lineHeight: 1.6 }}>
                      {t(point)}
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
