import React, { useEffect, useMemo, useState } from 'react';
import { FileText, Globe, Headphones, Image, ListVideo, MonitorPlay, Radio, Repeat, X } from 'lucide-react';
import { ActiveRelayStream, MediaKind } from '../../types';
import { KIND_LABELS, kindOf, relayUrl } from '../../media/media';
import { VideoPlayer } from './VideoPlayer';
import { AudioPlayer } from './AudioPlayer';
import { ImageViewer } from './ImageViewer';
import { DocumentViewer } from './DocumentViewer';
import { QueueControls } from './ControlBar';
import './player.css';

interface MediaStageProps {
  stream: ActiveRelayStream | null;
  onSelectStream: (stream: ActiveRelayStream) => void;
  onOpenInVault: (url: string) => void;
}

const KIND_ICONS: Record<MediaKind, React.ReactNode> = {
  video: <MonitorPlay size={13} />,
  audio: <Headphones size={13} />,
  image: <Image size={13} />,
  document: <FileText size={13} />,
};

// The media stage owns everything around a relayed item: which viewer
// presents it, whether its bytes take the protected relay or go direct, the
// retry after a failure, theater sizing and the session queue. Each new
// selection starts protected, and a failure never switches the path on its
// own; the operator chooses, and the choice is visible on the stage.
export const MediaStage: React.FC<MediaStageProps> = ({ stream, onSelectStream, onOpenInVault }) => {
  const [queue, setQueue] = useState<ActiveRelayStream[]>([]);
  const [queueOpen, setQueueOpen] = useState(false);
  const [autoAdvance, setAutoAdvance] = useState(true);
  const [theater, setTheater] = useState(false);
  const [protection, setProtection] = useState<{ stream: ActiveRelayStream | null; enabled: boolean }>({ stream: null, enabled: true });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!stream) return;
    setQueue((items) => (items.some((item) => item.streamUrl === stream.streamUrl) ? items : [...items, stream]));
  }, [stream]);

  const position = stream ? queue.findIndex((item) => item.streamUrl === stream.streamUrl) : -1;
  const isProtected = stream ? (protection.stream === stream ? protection.enabled : stream.useZeroTrustProxy) : true;
  const kind = stream ? kindOf(stream) : null;
  const src = stream ? (isProtected ? relayUrl(stream.streamUrl) : stream.streamUrl) : '';

  const retryProtected = () => {
    if (!stream) return;
    setProtection({ stream, enabled: true });
    setAttempt((count) => count + 1);
  };

  const queueControls = useMemo<QueueControls | undefined>(() => {
    if (!stream || position < 0) return undefined;
    return {
      position,
      length: queue.length,
      hasPrevious: position > 0,
      hasNext: position < queue.length - 1,
      onPrevious: () => onSelectStream(queue[position - 1]),
      onNext: () => onSelectStream(queue[position + 1]),
    };
  }, [stream, position, queue, onSelectStream]);

  const onEnded = () => {
    if (autoAdvance && queueControls?.hasNext) queueControls.onNext();
  };

  if (!stream || !kind) {
    return (
      <div className="media-stage media-stage--empty">
        <div className="media-empty">
          <Radio size={34} color="#38bdf8" />
          <h3>Protected media player</h3>
          <p>
            Choose a free, reachable source below and <strong>Try playback</strong>, pick an observed stream from the sandbox, or paste a media address above.
            Video and audio play through the relay with Shield on every request; images open in the zoomable viewer and PDFs in the reader.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className={`media-stage media-stage--${kind} ${theater ? 'media-stage--theater' : ''}`}>
      <div className="media-stage-view">
        {kind === 'video' && (
          <VideoPlayer
            key={stream.streamUrl}
            stream={stream}
            src={src}
            isProtected={isProtected}
            attempt={attempt}
            autoplay
            theater={theater}
            onToggleTheater={() => setTheater((value) => !value)}
            onRetryProtected={retryProtected}
            onEnded={onEnded}
            queue={queueControls}
          />
        )}
        {kind === 'audio' && (
          <AudioPlayer key={stream.streamUrl} stream={stream} src={src} isProtected={isProtected} attempt={attempt} autoplay onRetryProtected={retryProtected} onEnded={onEnded} queue={queueControls} />
        )}
        {kind === 'image' && (
          <ImageViewer key={stream.streamUrl} stream={stream} src={src} isProtected={isProtected} attempt={attempt} onRetryProtected={retryProtected} />
        )}
        {kind === 'document' && (
          <DocumentViewer key={stream.streamUrl} stream={stream} src={src} isProtected={isProtected} attempt={attempt} onRetryProtected={retryProtected} onOpenInVault={onOpenInVault} />
        )}
      </div>

      <div className="media-now">
        <div className="media-now-meta">
          <span className="media-now-kind">{KIND_ICONS[kind]} {KIND_LABELS[kind]}</span>
          <strong className="media-now-title" title={stream.title}>{stream.title}</strong>
          <span className="scout-muted">{stream.provider} · {stream.format}{stream.resolution ? ` · ${stream.resolution}` : ''}</span>
        </div>
        <div className="media-now-actions">
          <button
            type="button"
            className={`btn ${isProtected ? 'btn-theater' : 'btn-danger'}`}
            aria-pressed={isProtected}
            title="Every request for this item goes through the relay with Shield applied. Direct mode bypasses that filtering for this item only."
            onClick={() => setProtection({ stream, enabled: !isProtected })}
          >
            <Radio size={13} aria-hidden="true" /> {isProtected ? 'Ad filtering: ON' : 'Direct · filtering off'}
          </button>
          {stream.pageUrl && (
            <button type="button" className="btn" title="Open the source page in the protected browser" onClick={() => onOpenInVault(stream.pageUrl as string)}>
              <Globe size={13} aria-hidden="true" /> Open page with ad blocking
            </button>
          )}
          <button type="button" className={`btn ${queueOpen ? 'btn-primary' : ''}`} aria-pressed={queueOpen} onClick={() => setQueueOpen((open) => !open)} title="Items played this session">
            <ListVideo size={13} aria-hidden="true" /> Queue · {queue.length}
          </button>
        </div>
      </div>

      {queueOpen && (
        <div className="media-queue" aria-label="Session queue">
          <div className="media-queue-head">
            <span className="scout-muted">Items selected this session play in order. The current item cannot be removed.</span>
            <label className="media-queue-toggle">
              <input type="checkbox" checked={autoAdvance} onChange={(event) => setAutoAdvance(event.target.checked)} />
              <Repeat size={12} aria-hidden="true" /> Play next when finished
            </label>
          </div>
          <ol>
            {queue.map((item, index) => {
              const itemKind = kindOf(item);
              const current = index === position;
              return (
                <li key={item.streamUrl} className={current ? 'media-queue-item media-queue-item--current' : 'media-queue-item'}>
                  <button type="button" className="media-queue-select" aria-current={current ? 'true' : undefined} onClick={() => onSelectStream(item)}>
                    <span className="media-queue-index">{index + 1}</span>
                    <span className="media-now-kind">{KIND_ICONS[itemKind]}</span>
                    <span className="media-queue-text">
                      <strong>{item.title}</strong>
                      <span className="scout-muted">{item.provider} · {item.format}</span>
                    </span>
                  </button>
                  <button
                    type="button"
                    className="mp-btn"
                    aria-label={`Remove ${item.title} from the queue`}
                    disabled={current}
                    onClick={() => setQueue((items) => items.filter((entry) => entry.streamUrl !== item.streamUrl))}
                  >
                    <X size={14} />
                  </button>
                </li>
              );
            })}
          </ol>
        </div>
      )}
    </div>
  );
};
