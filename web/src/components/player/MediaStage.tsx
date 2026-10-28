import React, { useEffect, useMemo, useState } from 'react';
import { FileText, Globe, Headphones, Image, ListVideo, MonitorPlay, Radio, Repeat, X } from 'lucide-react';
import { ActiveRelayStream, MediaKind } from '../../types';
import { KIND_KEYS, kindOf, relayUrl } from '../../media/media';
import { VideoPlayer } from './VideoPlayer';
import { AudioPlayer } from './AudioPlayer';
import { ImageViewer } from './ImageViewer';
import { DocumentViewer } from './DocumentViewer';
import { QueueControls } from './ControlBar';
import { useI18n } from '../../i18n/core';
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
  const { t } = useI18n();
  const [queue, setQueue] = useState<ActiveRelayStream[]>([]);
  const [queueOpen, setQueueOpen] = useState(false);
  const [autoAdvance, setAutoAdvance] = useState(true);
  const [theater, setTheater] = useState(false);
  const [protection, setProtection] = useState<{ stream: ActiveRelayStream | null; enabled: boolean }>({ stream: null, enabled: true });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!stream) return;
    // A recording with several files brings them all: the selected track is
    // followed by the rest of the set, in the host's order, so the queue
    // plays the whole item when auto-advance is on.
    const incoming: ActiveRelayStream[] = [stream];
    const collection = stream.collection ?? stream.title;
    for (const track of stream.tracks ?? []) {
      if (track.url === stream.streamUrl) continue;
      incoming.push({
        title: `${track.title} · ${collection}`,
        collection,
        streamUrl: track.url,
        pageUrl: stream.pageUrl,
        provider: stream.provider,
        format: track.format ? t('stage.directFormat', { format: track.format }) : stream.format,
        useZeroTrustProxy: stream.useZeroTrustProxy,
        kind: 'audio',
        poster: stream.poster,
        durationSeconds: track.durationSeconds,
      });
    }
    setQueue((items) => {
      const known = new Set(items.map((item) => item.streamUrl));
      const added = incoming.filter((item) => !known.has(item.streamUrl));
      return added.length === 0 ? items : [...items, ...added];
    });
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
    const next = position < queue.length - 1 ? queue[position + 1] : null;
    return {
      position,
      length: queue.length,
      hasPrevious: position > 0,
      hasNext: next !== null,
      onPrevious: () => onSelectStream(queue[position - 1]),
      onNext: () => onSelectStream(queue[position + 1]),
      // The next item takes the path the current one takes, so the warmed
      // bytes are the bytes it will load.
      nextSrc: next ? (isProtected ? relayUrl(next.streamUrl) : next.streamUrl) : undefined,
    };
  }, [stream, position, queue, onSelectStream, isProtected]);

  const onEnded = () => {
    if (autoAdvance && queueControls?.hasNext) queueControls.onNext();
  };

  const streamActions = (current: ActiveRelayStream, protectedPath: boolean, compact = false) => (
    <>
      <button
        type="button"
        className={`btn mp-action ${protectedPath ? 'btn-theater' : 'btn-danger'}`}
        aria-label={protectedPath ? t('stage.protectedOnAria') : t('stage.protectedOffAria')}
        aria-pressed={protectedPath}
        title={t('stage.protectedTitle')}
        onClick={() => setProtection({ stream: current, enabled: !protectedPath })}
      >
        <Radio size={13} aria-hidden="true" />
        <span className="mp-action-label">{protectedPath ? t('stage.protectedOn') : t('stage.protectedOff')}</span>
      </button>
      {current.pageUrl && (
        <button
          type="button"
          className="btn mp-action"
          aria-label={t('stage.openPageAria')}
          title={t('stage.openPageTitle')}
          onClick={() => onOpenInVault(current.pageUrl as string)}
        >
          <Globe size={13} aria-hidden="true" />
          <span className="mp-action-label">{t('stage.openPage')}</span>
        </button>
      )}
      <button
        type="button"
        className={`btn mp-action ${queueOpen ? 'btn-primary' : ''}`}
        aria-label={t(queue.length === 1 ? 'stage.queueAria.one' : 'stage.queueAria.other', { count: queue.length })}
        aria-pressed={queueOpen}
        title={t('stage.queueTitle')}
        onClick={() => setQueueOpen((open) => !open)}
      >
        <ListVideo size={13} aria-hidden="true" />
        <span className="mp-action-label">{t('stage.queue', { count: queue.length })}</span>
        {compact && <span className="mp-action-count" aria-hidden="true">{queue.length}</span>}
      </button>
    </>
  );

  if (!stream || !kind) {
    return (
      <div className="media-stage media-stage--empty">
        <div className="media-empty">
          <Radio size={34} color="#38bdf8" />
          <h3>{t('stage.empty.title')}</h3>
          <p>
            {t('stage.empty.lead')} <strong>{t('stage.empty.strong')}</strong>{t('stage.empty.rest')}
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
            headerActions={streamActions(stream, isProtected, true)}
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

      {kind !== 'video' && (
        <div className="media-now">
          <div className="media-now-meta">
            <span className="media-now-kind">{KIND_ICONS[kind]} {t(KIND_KEYS[kind])}</span>
            <strong className="media-now-title" title={stream.title}>{stream.title}</strong>
            <span className="scout-muted">{stream.provider} · {stream.format}{stream.resolution ? ` · ${stream.resolution}` : ''}</span>
          </div>
          <div className="media-now-actions">{streamActions(stream, isProtected)}</div>
        </div>
      )}

      {queueOpen && (
        <div className="media-queue" aria-label={t('stage.queueHeading')}>
          <div className="media-queue-head">
            <span className="scout-muted">{t('stage.queueNote')}</span>
            <label className="media-queue-toggle">
              <input type="checkbox" checked={autoAdvance} onChange={(event) => setAutoAdvance(event.target.checked)} />
              <Repeat size={12} aria-hidden="true" /> {t('stage.playNext')}
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
                    aria-label={t('stage.remove', { title: item.title })}
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
