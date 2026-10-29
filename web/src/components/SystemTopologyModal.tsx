import React, { useState } from 'react';
import { MessageKey, useI18n } from '../i18n/core';
import { useDialog } from './useDialog';

interface SystemTopologyModalProps {
  onClose: () => void;
}

interface TopologyStep {
  step: number;
  daemon: string;
  color: string;
  title: MessageKey;
  boundary: MessageKey;
  detail: MessageKey;
}

const TOPOLOGY_STEPS: TopologyStep[] = [
  { step: 1, daemon: 'itemba-scout', color: 'var(--text-secondary)', title: 'topology.step1.title', boundary: 'topology.step1.boundary', detail: 'topology.step1.detail' },
  { step: 2, daemon: 'itemba-shield (Layer 1)', color: 'var(--warn-text)', title: 'topology.step2.title', boundary: 'topology.step2.boundary', detail: 'topology.step2.detail' },
  { step: 3, daemon: 'itemba-shield (Layer 2)', color: 'var(--accent-text)', title: 'topology.step3.title', boundary: 'topology.step3.boundary', detail: 'topology.step3.detail' },
  { step: 4, daemon: 'itemba-shield (Layer 3)', color: 'var(--danger-text)', title: 'topology.step4.title', boundary: 'topology.step4.boundary', detail: 'topology.step4.detail' },
  { step: 5, daemon: 'itemba-vault', color: 'var(--ok-text)', title: 'topology.step5.title', boundary: 'topology.step5.boundary', detail: 'topology.step5.detail' },
];

export const SystemTopologyModal: React.FC<SystemTopologyModalProps> = ({ onClose }) => {
  const { t } = useI18n();
  const [activeStep, setActiveStep] = useState<number>(1);
  const dialogRef = useDialog<HTMLDivElement>(onClose);

  const current = TOPOLOGY_STEPS.find((step) => step.step === activeStep) || TOPOLOGY_STEPS[0];

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        ref={dialogRef}
        className="modal-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby="topology-title"
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 18 }}>
          <div>
            <h2 id="topology-title" style={{ fontSize: 18, fontWeight: 800, color: 'var(--text-primary)' }}>
              {t('topology.title')}
            </h2>
            <p style={{ fontSize: 12.5, color: 'var(--text-secondary)', marginTop: 2 }}>
              {t('topology.intro')}
            </p>
          </div>
          <button type="button" className="btn" onClick={onClose}>
            ✕ {t('app.close')}
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
              <button
                key={item.step}
                type="button"
                aria-pressed={isSelected}
                onClick={() => setActiveStep(item.step)}
                style={{
                  background: isSelected ? 'rgba(82, 170, 224, 0.14)' : 'var(--bg-card)',
                  border: `1px solid ${isSelected ? item.color : 'var(--border-subtle)'}`,
                  borderRadius: 10,
                  padding: 12,
                  cursor: 'pointer',
                  transition: 'all 0.15s',
                  textAlign: 'start',
                  width: '100%',
                  font: 'inherit',
                  color: 'inherit',
                }}
              >
                <span
                  style={{
                    display: 'block',
                    fontSize: 10,
                    fontFamily: 'monospace',
                    fontWeight: 800,
                    color: item.color,
                    marginBottom: 4,
                  }}
                >
                  {t('topology.stage', { step: item.step })} • {item.daemon}
                </span>
                <span style={{ display: 'block', fontSize: 12.5, fontWeight: 700, color: 'var(--text-primary)' }}>{t(item.title)}</span>
              </button>
            );
          })}
        </div>

        {/* Active Stage Inspector */}
        <div
          style={{
            background: 'var(--bg-inset)',
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
              {t('topology.daemon', { daemon: current.daemon })}
            </span>
            <span className="pill-badge">{t(current.boundary)}</span>
          </div>
          <h3 style={{ fontSize: 17, fontWeight: 800, color: 'var(--text-primary)', marginBottom: 8 }}>{t(current.title)}</h3>
          <p style={{ fontSize: 13.5, color: 'var(--text-secondary)', lineHeight: 1.65 }}>{t(current.detail)}</p>
        </div>
      </div>
    </div>
  );
};
