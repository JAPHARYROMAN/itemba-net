import React, { useCallback, useRef, useState } from 'react';
import { BufferedRange } from '../../media/useMediaEngine';
import { clamp, formatTime } from '../../media/media';
import { useI18n } from '../../i18n/core';

interface SeekBarProps {
  currentTime: number;
  duration: number;
  buffered: BufferedRange[];
  live: boolean;
  disabled?: boolean;
  onSeek: (seconds: number) => void;
}

// The timeline: buffered ranges, the played span, a hover time and a thumb
// that can be dragged. Pointer capture keeps a drag alive when the pointer
// leaves the bar; the seek is committed on release so a long scrub does not
// fire a seek per pixel.
export const SeekBar: React.FC<SeekBarProps> = ({ currentTime, duration, buffered, live, disabled, onSeek }) => {
  const { t } = useI18n();
  const barRef = useRef<HTMLDivElement | null>(null);
  const [hoverRatio, setHoverRatio] = useState<number | null>(null);
  const [scrubRatio, setScrubRatio] = useState<number | null>(null);
  const seekable = !disabled && !live && Number.isFinite(duration) && duration > 0;

  const ratioAt = useCallback((clientX: number): number => {
    const bar = barRef.current;
    if (!bar) return 0;
    const rect = bar.getBoundingClientRect();
    if (rect.width <= 0) return 0;
    return clamp((clientX - rect.left) / rect.width, 0, 1);
  }, []);

  const playedRatio = seekable ? clamp(currentTime / duration, 0, 1) : 0;
  const shownRatio = scrubRatio ?? playedRatio;
  const tooltipRatio = scrubRatio ?? hoverRatio;

  return (
    <div
      ref={barRef}
      className={`mp-seek ${seekable ? '' : 'mp-seek--inert'} ${scrubRatio !== null ? 'mp-seek--scrubbing' : ''}`}
      role="slider"
      aria-label={t('player.seek')}
      aria-valuemin={0}
      aria-valuemax={seekable ? Math.round(duration) : 0}
      aria-valuenow={seekable ? Math.round(currentTime) : 0}
      aria-valuetext={seekable ? t('player.of', { current: formatTime(currentTime), duration: formatTime(duration) }) : live ? t('player.live') : t('player.notSeekable')}
      aria-disabled={!seekable}
      tabIndex={-1}
      onPointerMove={(event) => {
        if (!seekable) return;
        const ratio = ratioAt(event.clientX);
        if (scrubRatio !== null) setScrubRatio(ratio);
        else setHoverRatio(ratio);
      }}
      onPointerLeave={() => setHoverRatio(null)}
      onPointerDown={(event) => {
        if (!seekable || event.button !== 0) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        setScrubRatio(ratioAt(event.clientX));
      }}
      onPointerUp={(event) => {
        if (scrubRatio === null) return;
        const ratio = ratioAt(event.clientX);
        setScrubRatio(null);
        setHoverRatio(null);
        onSeek(ratio * duration);
      }}
      onPointerCancel={() => setScrubRatio(null)}
    >
      <div className="mp-seek-track">
        {seekable && buffered.map((range, index) => (
          <div
            key={index}
            className="mp-seek-buffered"
            style={{ left: `${clamp(range.start / duration, 0, 1) * 100}%`, width: `${clamp((range.end - range.start) / duration, 0, 1) * 100}%` }}
          />
        ))}
        <div className="mp-seek-played" style={{ width: `${shownRatio * 100}%` }} />
        {seekable && <div className="mp-seek-thumb" style={{ left: `${shownRatio * 100}%` }} />}
      </div>
      {seekable && tooltipRatio !== null && (
        <div className="mp-seek-tooltip" style={{ left: `${tooltipRatio * 100}%` }}>{formatTime(tooltipRatio * duration)}</div>
      )}
      {live && <span className="mp-seek-live">{t('player.live')}</span>}
    </div>
  );
};
