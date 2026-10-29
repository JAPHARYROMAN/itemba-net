import React from 'react';

interface ChunkBoundaryProps {
  fallback: React.ReactNode;
  children: React.ReactNode;
}

interface ChunkBoundaryState {
  failed: boolean;
}

// A rejected dynamic import — an offline moment, or a stale chunk hash after a
// deploy — would otherwise unmount the whole console. This keeps the failure
// inside the panel that was loading; a reload is the one recovery that also
// picks up the current chunk hashes.
export class ChunkBoundary extends React.Component<ChunkBoundaryProps, ChunkBoundaryState> {
  state: ChunkBoundaryState = { failed: false };

  static getDerivedStateFromError(): ChunkBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: unknown) {
    console.error('A lazy console panel failed to load', error);
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

interface ChunkErrorProps {
  title: string;
  body: string;
  action: string;
  onReload: () => void;
  dismissLabel?: string;
  onDismiss?: () => void;
  className?: string;
}

export const ChunkError: React.FC<ChunkErrorProps> = ({
  title,
  body,
  action,
  onReload,
  dismissLabel,
  onDismiss,
  className,
}) => (
  <div className={`chunk-error ${className ?? ''}`.trim()} role="alert">
    <div className="chunk-error-card">
      <strong>{title}</strong>
      <p>{body}</p>
      <div className="chunk-error-actions">
        <button type="button" className="btn" onClick={onReload} autoFocus>{action}</button>
        {onDismiss && dismissLabel && (
          <button type="button" className="btn" onClick={onDismiss}>{dismissLabel}</button>
        )}
      </div>
    </div>
  </div>
);
