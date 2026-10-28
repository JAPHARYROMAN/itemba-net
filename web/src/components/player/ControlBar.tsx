import React from 'react';
import {
  Activity,
  AudioLines,
  Captions,
  Gauge,
  Keyboard,
  Layers,
  Maximize,
  Minimize,
  Pause,
  PictureInPicture2,
  Play,
  RectangleHorizontal,
  Rewind,
  FastForward,
  RotateCcw,
  SkipBack,
  SkipForward,
  Volume1,
  Volume2,
  VolumeX,
} from 'lucide-react';
import { EngineActions, EngineState } from '../../media/useMediaEngine';
import { formatTime } from '../../media/media';
import { Menu } from './Menu';
import { SeekBar } from './SeekBar';
import { useI18n } from '../../i18n/core';

// Where the current item sits in the session queue and how to move along it.
export interface QueueControls {
  position: number;
  length: number;
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
  // nextSrc is the address the next item would load from, so a player can
  // warm it before the current item ends.
  nextSrc?: string;
}

export interface ControlBarFeatures {
  quality?: boolean;
  captions?: boolean;
  audioTracks?: boolean;
  pictureInPicture?: boolean;
  theater?: boolean;
  fullscreen?: boolean;
  stats?: boolean;
  help?: boolean;
}

interface ControlBarProps {
  state: EngineState;
  actions: EngineActions;
  features: ControlBarFeatures;
  queue?: QueueControls;
  theater?: boolean;
  onToggleTheater?: () => void;
  fullscreen?: boolean;
  onToggleFullscreen?: () => void;
  pictureInPicture?: boolean;
  onTogglePictureInPicture?: () => void;
  statsOpen?: boolean;
  onToggleStats?: () => void;
  helpOpen?: boolean;
  onToggleHelp?: () => void;
}

const RATES = [0.5, 0.75, 1, 1.25, 1.5, 2];
const SKIP_SECONDS = 10;

// The transport and settings row shared by the video and audio players.
// Every control reads from the engine state and writes through its actions,
// so what the bar shows is what the element is doing.
export const ControlBar: React.FC<ControlBarProps> = ({
  state,
  actions,
  features,
  queue,
  theater,
  onToggleTheater,
  fullscreen,
  onToggleFullscreen,
  pictureInPicture,
  onTogglePictureInPicture,
  statsOpen,
  onToggleStats,
  helpOpen,
  onToggleHelp,
}) => {
  const { t } = useI18n();
  const playing = state.status === 'playing';
  const ended = state.status === 'ended';
  const inert = state.status === 'idle' || state.status === 'error';
  const VolumeIcon = state.muted || state.volume === 0 ? VolumeX : state.volume < 0.5 ? Volume1 : Volume2;
  const activeLevel = state.levels.find((level) => level.id === state.activeLevel);

  return (
    <div className="mp-controls" onPointerDown={(event) => event.stopPropagation()} onDoubleClick={(event) => event.stopPropagation()}>
      <SeekBar
        currentTime={state.currentTime}
        duration={state.duration}
        buffered={state.buffered}
        live={state.live}
        disabled={inert}
        onSeek={actions.seek}
      />
      <div className="mp-controls-row">
        {queue && (
          <button type="button" className="mp-btn" aria-label={t('player.previous')} title={t('player.previous')} disabled={!queue.hasPrevious} onClick={queue.onPrevious}>
            <SkipBack size={17} />
          </button>
        )}
        <button
          type="button"
          className="mp-btn mp-btn--play"
          aria-label={playing ? t('player.pause') : ended ? t('player.replay') : t('player.play')}
          title={playing ? t('player.pauseShort') : ended ? t('player.replayShort') : t('player.playShort')}
          disabled={inert}
          onClick={actions.toggle}
        >
          {playing ? <Pause size={20} /> : ended ? <RotateCcw size={19} /> : <Play size={20} />}
        </button>
        {queue && (
          <button type="button" className="mp-btn" aria-label={t('player.next')} title={t('player.next')} disabled={!queue.hasNext} onClick={queue.onNext}>
            <SkipForward size={17} />
          </button>
        )}
        <button type="button" className="mp-btn" aria-label={t('player.back', { seconds: SKIP_SECONDS })} title={t('player.backShort', { seconds: SKIP_SECONDS })} disabled={inert || state.live} onClick={() => actions.seekBy(-SKIP_SECONDS)}>
          <Rewind size={17} />
        </button>
        <button type="button" className="mp-btn" aria-label={t('player.forward', { seconds: SKIP_SECONDS })} title={t('player.forwardShort', { seconds: SKIP_SECONDS })} disabled={inert || state.live} onClick={() => actions.seekBy(SKIP_SECONDS)}>
          <FastForward size={17} />
        </button>

        <div className="mp-volume">
          <button type="button" className="mp-btn" aria-label={state.muted ? t('player.unmute') : t('player.mute')} title={state.muted ? t('player.unmuteShort') : t('player.muteShort')} onClick={actions.toggleMute}>
            <VolumeIcon size={17} />
          </button>
          <input
            className="mp-volume-slider"
            type="range"
            min={0}
            max={1}
            step={0.02}
            value={state.muted ? 0 : state.volume}
            aria-label={t('player.volume')}
            onChange={(event) => actions.setVolume(Number(event.target.value))}
          />
        </div>

        <span className="mp-time" aria-live="off">
          {state.live ? t('player.live') : t('player.position', { current: formatTime(state.currentTime), duration: formatTime(state.duration) })}
          {queue && queue.length > 1 && <span className="mp-time-queue">{t('player.queuePosition', { position: queue.position + 1, length: queue.length })}</span>}
        </span>

        <span className="mp-controls-spacer" />

        {features.captions && (
          <Menu
            label={t('player.captions')}
            title={state.subtitleTracks.length === 0 ? t('player.captionsNone') : t('player.captionsTitle')}
            icon={<Captions size={17} />}
            active={state.subtitleTrack >= 0}
            items={[
              { id: 'off', label: t('player.off'), selected: state.subtitleTrack < 0, onSelect: () => actions.setSubtitleTrack(-1) },
              ...state.subtitleTracks.map((track) => ({ id: track.id, label: track.label, hint: track.lang, selected: state.subtitleTrack === track.id, onSelect: () => actions.setSubtitleTrack(track.id) })),
            ]}
            disabled={state.subtitleTracks.length === 0}
          />
        )}

        {features.audioTracks && state.audioTracks.length > 1 && (
          <Menu
            label={t('player.audioTrack')}
            icon={<AudioLines size={17} />}
            items={state.audioTracks.map((track) => ({ id: track.id, label: track.label, hint: track.lang, selected: state.audioTrack === track.id, onSelect: () => actions.setAudioTrack(track.id) }))}
          />
        )}

        {features.quality && state.levels.length > 0 && (
          <Menu
            label={t('player.quality')}
            title={t('player.quality')}
            icon={<Layers size={17} />}
            caption={activeLevel && activeLevel.height > 0 ? `${activeLevel.height}p` : undefined}
            items={[
              {
                id: 'auto',
                label: t('player.auto'),
                hint: activeLevel && state.levelChoice === -1 ? (activeLevel.height > 0 ? t('player.nowAt', { resolution: `${activeLevel.height}p` }) : undefined) : undefined,
                selected: state.levelChoice === -1,
                onSelect: () => actions.setLevel(-1),
              },
              ...[...state.levels].sort((a, b) => b.height - a.height || b.bitrateKbps - a.bitrateKbps).map((level) => ({
                id: level.id,
                label: level.label,
                selected: state.levelChoice === level.id,
                onSelect: () => actions.setLevel(level.id),
              })),
            ]}
          />
        )}

        <Menu
          label={t('player.speed')}
          title={t('player.speedTitle')}
          icon={<Gauge size={17} />}
          caption={state.rate !== 1 ? `${state.rate}×` : undefined}
          items={RATES.map((rate) => ({ id: rate, label: rate === 1 ? t('player.normal') : `${rate}×`, selected: Math.abs(state.rate - rate) < 0.001, onSelect: () => actions.setRate(rate) }))}
        />

        {features.stats && onToggleStats && (
          <button type="button" className={`mp-btn ${statsOpen ? 'mp-btn--active' : ''}`} aria-pressed={statsOpen} aria-label={t('player.stats')} title={t('player.statsShort')} onClick={onToggleStats}>
            <Activity size={17} />
          </button>
        )}
        {features.help && onToggleHelp && (
          <button type="button" className={`mp-btn ${helpOpen ? 'mp-btn--active' : ''}`} aria-pressed={helpOpen} aria-label={t('player.shortcuts')} title={t('player.shortcutsShort')} onClick={onToggleHelp}>
            <Keyboard size={17} />
          </button>
        )}
        {features.pictureInPicture && onTogglePictureInPicture && (
          <button type="button" className={`mp-btn ${pictureInPicture ? 'mp-btn--active' : ''}`} aria-pressed={pictureInPicture} aria-label={t('player.pip')} title={t('player.pipShort')} disabled={inert} onClick={onTogglePictureInPicture}>
            <PictureInPicture2 size={17} />
          </button>
        )}
        {features.theater && onToggleTheater && (
          <button type="button" className={`mp-btn mp-btn--theater ${theater ? 'mp-btn--active' : ''}`} aria-pressed={theater} aria-label={t('player.theater')} title={t('player.theaterShort')} onClick={onToggleTheater}>
            <RectangleHorizontal size={17} />
          </button>
        )}
        {features.fullscreen && onToggleFullscreen && (
          <button type="button" className="mp-btn" aria-label={fullscreen ? t('player.exitFullscreen') : t('player.fullscreen')} title={fullscreen ? t('player.exitFullscreenShort') : t('player.fullscreenShort')} onClick={onToggleFullscreen}>
            {fullscreen ? <Minimize size={17} /> : <Maximize size={17} />}
          </button>
        )}
      </div>
    </div>
  );
};
