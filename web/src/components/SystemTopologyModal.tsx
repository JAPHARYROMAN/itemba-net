import React, { useState } from 'react';
import { MessageKey, useI18n } from '../i18n/core';

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
  { step: 1, daemon: 'itemba-scout', color: '#38bdf8', title: 'topology.step1.title', boundary: 'topology.step1.boundary', detail: 'topology.step1.detail' },
  { step: 2, daemon: 'itemba-shield (Layer 1)', color: '#f59e0b', title: 'topology.step2.title', boundary: 'topology.step2.boundary', detail: 'topology.step2.detail' },
  { step: 3, daemon: 'itemba-shield (Layer 2)', color: '#a855f7', title: 'topology.step3.title', boundary: 'topology.step3.boundary', detail: 'topology.step3.detail' },
  { step: 4, daemon: 'itemba-shield (Layer 3)', color: '#f43f5e', title: 'topology.step4.title', boundary: 'topology.step4.boundary', detail: 'topology.step4.detail' },
  { step: 5, daemon: 'itemba-vault', color: '#10b981', title: 'topology.step5.title', boundary: 'topology.step5.boundary', detail: 'topology.step5.detail' },
];

export const SystemTopologyModal: React.FC<SystemTopologyModalProps> = ({ onClose }) => {
  const { t } = useI18n();
  const [activeStep, setActiveStep] = useState<number>(1);

  const current = TOPOLOGY_STEPS.find((step) => step.step === activeStep) || TOPOLOGY_STEPS[0];

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(event) => event.stopPropagation()}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 18 }}>
          <div>
            <h2 style={{ fontSize: 18, fontWeight: 800, color: '#f8fafc' }}>
              {t('topology.title')}
            </h2>
            <p style={{ fontSize: 12.5, color: '#94a3b8', marginTop: 2 }}>
              {t('topology.intro')}
            </p>
          </div>
          <button className="btn" onClick={onClose}>
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
                  {t('topology.stage', { step: item.step })} • {item.daemon}
                </div>
                <div style={{ fontSize: 12.5, fontWeight: 700, color: '#f1f5f9' }}>{t(item.title)}</div>
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
              {t('topology.daemon', { daemon: current.daemon })}
            </span>
            <span className="pill-badge">{t(current.boundary)}</span>
          </div>
          <h3 style={{ fontSize: 17, fontWeight: 800, color: '#f8fafc', marginBottom: 8 }}>{t(current.title)}</h3>
          <p style={{ fontSize: 13.5, color: '#cbd5e1', lineHeight: 1.65 }}>{t(current.detail)}</p>
        </div>
      </div>
    </div>
  );
};
