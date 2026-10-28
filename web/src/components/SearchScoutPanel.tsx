import React, { useEffect, useState } from 'react';
import { Play, Globe, Search, Shield, Clock, Sparkles, Film, Music, Fingerprint, Image as ImageIcon, Scan, FileText, BookOpen } from 'lucide-react';
import { ActiveRelayStream, AudioTrack, DocumentCheck, FawesomeShelfPlaybackReport, ImageCheck, MovieAvailabilityCheck, MovieDiscoveryItem, MovieDiscoveryMode, MovieDiscoveryReport, RenderCheck, SearchKind, SearchKnownEvent, SearchMergedEvent, SearchReport, VerifiedSource } from '../types';
import { RankedLocations } from './RankedLocations';
import { FormatMatrix } from './FormatMatrix';
import { IndexResolution } from './IndexResolution';
import { fingerprintLabel, identityLabel } from '../identity';
import { MessageKey, Translate, useI18n } from '../i18n/core';
import { isConvertibleUrl, isPdfUrl } from '../media/media';

interface SearchScoutPanelProps {
  report: SearchReport | null;
  known: SearchKnownEvent | null;
  merged: SearchMergedEvent | null;
  searching: boolean;
  searchError: string | null;
  launchError: string | null;
  launchingUrl: string | null;
  onSearch: (query: string, region?: string, fullLengthOnly?: boolean, kind?: SearchKind) => void;
  onRunBenchmark: () => void;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

type RegionFormatter = (code?: string, fallback?: string) => string | null;

const REGIONS = [
  ['ALL', 'Worldwide'], ['US', 'United States'], ['GB', 'United Kingdom'],
  ['IN', 'India'], ['ZA', 'South Africa'], ['KE', 'Kenya'], ['TZ', 'Tanzania'],
  ['CA', 'Canada'], ['AU', 'Australia'], ['DE', 'Germany'], ['FR', 'France'],
];
const FREE_CATALOG_EXAMPLES = ['Seder-Masochism 2018', 'Planetary Defenders 2025', 'The Secret Wife 2024'];
const PROVIDER_MOVIE_BASES = new Set(['filmzie_movie_metadata', 'nasa_movie_metadata', 'creator_movie_metadata', 'fawesome_movie_metadata', 'nfb_movie_metadata']);
const ACCESS_KEYS: Record<string, MessageKey> = {
  free: 'access.free', subscription: 'access.subscription', rent: 'access.rental', buy: 'access.purchase', paid: 'access.paid', unknown: 'access.unknown',
};
const PLAYBACK_REASON_KEYS: Record<string, MessageKey> = {
  region_unavailable: 'reason.region_unavailable',
  sign_in_required: 'reason.sign_in_required',
  drm_required: 'reason.drm_required',
  content_unconfirmed: 'reason.content_unconfirmed',
  trailer_only: 'reason.trailer_only',
  blocked_with_protection: 'reason.blocked_with_protection',
  timeout: 'reason.timeout',
  ads_only: 'reason.ads_only',
  expired: 'reason.expired',
  protection_changed: 'reason.protection_changed',
  location_changed: 'reason.location_changed',
};
type AccessFilter = 'all' | 'free' | 'paid' | 'unknown';
const FILTER_KEYS: Record<AccessFilter, MessageKey> = {
  all: 'search.filter.all', free: 'search.filter.free', paid: 'search.filter.paid', unknown: 'search.filter.unknown',
};
const FILTERS: AccessFilter[] = ['all', 'free', 'paid', 'unknown'];

const accessGroup = (source: VerifiedSource): AccessFilter => {
  if (source.accessType === 'free') return 'free';
  if (source.accessType && source.accessType !== 'unknown') return 'paid';
  return 'unknown';
};

const marketCode = (market?: string): string | null => {
  const code = market?.trim().toUpperCase();
  return code && /^[A-Z]{2}$/.test(code) ? code : null;
};

const countryName = (country: string | undefined, regionName: RegionFormatter): string | null => {
  const code = marketCode(country);
  if (!code) return null;
  return regionName(code, REGIONS.find(([region]) => region === code)?.[1]);
};

const accessSummary = (source: VerifiedSource, t: Translate): string => {
  if (source.accessType === 'free' && PROVIDER_MOVIE_BASES.has(source.accessBasis ?? '')) return t('access.summary.providerMovie');
  if (source.accessType === 'free' && (source.accessBasis === 'archive_metadata' || source.accessBasis === 'peertube_api')) return t('access.summary.freeCatalog');
  if (source.accessBasis === 'creator_catalog') return t('access.summary.creatorCatalog');
  if (source.accessBasis === 'provider_model') return t('access.summary.providerModel');
  if (source.accessBasis === 'catalog' && source.price) return source.price;
  if (accessGroup(source) === 'unknown') return t('access.summary.unknown');
  if (source.accessBasis === 'catalog') return t(source.accessType === 'free' ? 'access.summary.catalogFree' : 'access.summary.catalogPaid');
  if (source.accessBasis === 'page_text') return t(source.accessType === 'free' ? 'access.summary.pageFree' : 'access.summary.pagePaid');
  return t(source.accessType === 'free' ? 'access.summary.listedFree' : 'access.summary.listedPaid');
};

const availabilityLabel = (source: VerifiedSource, country: string, countryLabel: string, t: Translate, regionName: RegionFormatter): string => {
  const market = marketCode(source.region);
  const marketName = market ? countryName(market, regionName) ?? market : null;
  if (country === 'ALL') {
    switch (source.availabilityStatus) {
      case 'regional_listing': return marketName ? t('availability.regionalListing', { market: marketName }) : t('availability.regionalListingFallback');
      case 'service_available': return t('availability.serviceAvailable');
      case 'other_region': return marketName ? t('availability.otherRegion', { market: marketName }) : t('availability.otherRegionFallback');
      case 'unavailable': return t('availability.unavailable');
      default: return t('availability.unconfirmed');
    }
  }
  switch (source.availabilityStatus) {
    case 'regional_listing': return market && market !== country ? t('availability.marketOther', { market, country: countryLabel }) : t('availability.regionalListingCountry', { country: countryLabel });
    case 'service_available': return t('availability.serviceInCountry', { country: countryLabel });
    case 'other_region': return t('availability.otherCountry', { country: market || t('availability.otherMarket'), country2: countryLabel });
    case 'unavailable': return t('availability.notSupported', { country: countryLabel });
    default: return t('availability.countryUnconfirmed', { country: countryLabel });
  }
};

const sourceHost = (url: string): string => {
  try { return new URL(url).hostname.replace(/^www\./, ''); } catch { return url; }
};

const runtimeSeconds = (value?: string): number | null => {
  if (!value?.trim()) return null;
  const text = value.trim();
  if (/^\d+(?:\.\d+)?$/.test(text)) {
    const seconds = Number(text);
    return Number.isFinite(seconds) && seconds > 0 ? seconds : null;
  }
  const clock = text.split(':');
  if (clock.length >= 2 && clock.length <= 3 && clock.every((part, index) => /^\d+(?:\.\d+)?$/.test(part) && (index === 0 || Number(part) < 60))) {
    const seconds = clock.reduce((total, part) => total * 60 + Number(part), 0);
    return Number.isFinite(seconds) && seconds > 0 ? seconds : null;
  }
  const duration = /^(?:(\d+(?:\.\d+)?)h)?\s*(?:(\d+(?:\.\d+)?)m)?\s*(?:(\d+(?:\.\d+)?)s)?$/.exec(text);
  if (!duration || !duration.slice(1).some(Boolean)) return null;
  const seconds = Number(duration[1] ?? 0) * 3600 + Number(duration[2] ?? 0) * 60 + Number(duration[3] ?? 0);
  return Number.isFinite(seconds) && seconds > 0 ? seconds : null;
};

const formatRuntime = (seconds: number, t: Translate): string => {
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  return hours > 0
    ? t('runtime.hoursMinutes', { hours, minutes: minutes % 60 })
    : minutes > 0 ? t('runtime.minutes', { minutes }) : t('runtime.seconds', { seconds: Math.floor(seconds) });
};

const accessCheckLabel = (check: MovieAvailabilityCheck | undefined, t: Translate, regionName: RegionFormatter): string => {
  if (check?.method === 'not_checked') return t('check.unchecked');
  const location = countryName(check?.country, regionName);
  const checkedFrom = location ? t('search.linkChecks.from', { location }) : t('search.linkChecks.locationUnknown');
  switch (check?.status) {
    case 'media_reachable': return `${t('check.mediaReachable')}${checkedFrom}`;
    case 'page_reachable': return `${t('check.pageReachable')}${checkedFrom}`;
    case 'region_blocked': return t('check.regionBlocked');
    case 'not_found': return t('check.notFound');
    case 'sign_in_required': return t('check.signInRequired');
    case 'access_restricted': return t('check.accessRestricted');
    case 'unconfirmed': return t('check.unconfirmed');
    case 'error': return t('check.error');
    default: return t('check.unchecked');
  }
};

const playbackSampleStarted = (source: Pick<VerifiedSource, 'accessType' | 'playbackCheck'>): boolean => {
  const check = source.playbackCheck;
  return source.accessType === 'free' && check?.status === 'started' && check.playbackConfirmed === true
    && check.method === 'isolated_video_sample' && !check.reason
    && Number.isFinite(check.secondsAdvanced) && (check.secondsAdvanced ?? 0) >= 1
    && Number.isFinite(check.decodedFrames) && (check.decodedFrames ?? 0) > 0
    && Number.isFinite(check.width) && (check.width ?? 0) > 0
    && Number.isFinite(check.height) && (check.height ?? 0) > 0;
};

const playbackCheckLabel = (source: VerifiedSource, region: string, t: Translate, regionName: RegionFormatter): string => {
  if (source.accessType !== 'free') return t('playback.skippedPaid');
  const check = source.playbackCheck;
  if (playbackSampleStarted(source)) {
    if (region === 'ALL') return t('playback.started');
    const location = countryName(check?.country, regionName);
    return location ? t('playback.startedFrom', { location }) : t('playback.startedLocationUnknown');
  }
  const reasonKey = check?.reason ? PLAYBACK_REASON_KEYS[check.reason] : undefined;
  if (reasonKey) return t(reasonKey);
  if (check?.method === 'provider_playback_preflight') return t('playback.providerPreflight');
  switch (check?.status) {
    case 'not_started': return t('playback.notStarted');
    case 'unsupported': return t('playback.unsupported');
    case 'unconfirmed': return t('playback.unconfirmed');
    case 'skipped': return t('playback.skipped');
    case 'started': return t('playback.startedUnconfirmed');
    case 'expired': return t('playback.expired');
    default: return t('playback.notTested');
  }
};

const playbackMetrics = (source: VerifiedSource, t: Translate, formatNumber: (value: number) => string): string => {
  const sample = source.playbackCheck;
  if (!sample || !playbackSampleStarted(source)) return '';
  const metrics: string[] = [];
  if (sample.secondsAdvanced && Number.isFinite(sample.secondsAdvanced) && sample.secondsAdvanced > 0) metrics.push(t('playback.metrics.progress', { seconds: sample.secondsAdvanced.toFixed(1) }));
  if (sample.width && sample.height && Number.isFinite(sample.width) && Number.isFinite(sample.height) && sample.width > 0 && sample.height > 0) metrics.push(`${formatNumber(sample.width)}×${formatNumber(sample.height)}`);
  return metrics.join(' · ');
};

const canTryFreeMedia = (source: VerifiedSource): boolean =>
  source.accessType === 'free' && Boolean(source.directStreamUrl || source.detectedManifests?.length)
  && !['ads_only', 'trailer_only', 'drm_required', 'sign_in_required', 'blocked_with_protection'].includes(source.playbackCheck?.reason ?? '')
  && (playbackSampleStarted(source) || (source.verificationStatus === 'media_reachable'
    && (!source.availabilityCheck || source.availabilityCheck.status === 'media_reachable')));

const isFawesomeMovieSuggestion = (raw: string): boolean => {
  try {
    const url = new URL(raw);
    return url.protocol === 'https:' && url.hostname.replace(/^www\./, '') === 'fawesome.tv'
      && !url.username && !url.password && !url.search && !url.hash
      && /^\/movies\/[1-9]\d{0,11}\/[a-z0-9]+(?:-[a-z0-9]+)*\/?$/.test(url.pathname);
  } catch { return false; }
};

// Shelf failures are kept as message keys, not composed strings, so a
// language switch re-renders the notice without restarting the round.
class ShelfError extends Error {
  constructor(readonly key: MessageKey) { super(key); }
}

const shelfFailure = (error: unknown): MessageKey =>
  error instanceof ShelfError ? error.key : 'shelf.error.suggestions';

const shelfPlaybackStarted = (movie: MovieDiscoveryItem, playback: FawesomeShelfPlaybackReport | undefined, now: number): boolean =>
  playback?.adBlockingEnabled === true && movie.playbackState === 'checked' && playbackSampleStarted(movie)
  && Number.isFinite(movie.playbackCheck?.durationSeconds) && (movie.playbackCheck?.durationSeconds ?? 0) >= 40 * 60
  && Number.isFinite(Date.parse(movie.playbackExpiresAt ?? '')) && Date.parse(movie.playbackExpiresAt ?? '') > now;

const shelfPlaybackLabel = (movie: MovieDiscoveryItem, playback: FawesomeShelfPlaybackReport | undefined, now: number, t: Translate): string => {
  if (shelfPlaybackStarted(movie, playback, now)) return t('shelf.playback.started');
  if (movie.playbackState === 'expired' || Date.parse(movie.playbackExpiresAt ?? '') <= now) return t('shelf.playback.expired');
  const running = playback?.state === 'running' && Date.parse(playback.expiresAt) > now;
  if (running && movie.playbackState === 'checking') return t('shelf.playback.checking');
  if (running && movie.playbackState === 'queued') return t('shelf.playback.queued');
  if (movie.playbackState === 'checked') {
    const reason = movie.playbackCheck?.reason;
    return reason ? t(PLAYBACK_REASON_KEYS[reason]) : t('shelf.playback.unconfirmed');
  }
  return t('shelf.playback.unchecked');
};

const MovieDiscoveryShelf: React.FC<{ region: string; onSelect: (query: string) => void }> = ({ region, onSelect }) => {
  const { t, formatDateTime, regionName } = useI18n();
  const [source, setSource] = useState<'charts' | 'fawesome'>('charts');
  const [mode, setMode] = useState<MovieDiscoveryMode>('popular');
  const [retry, setRetry] = useState(0);
  const [visibleLimit, setVisibleLimit] = useState(6);
  const [focusedMovieId, setFocusedMovieId] = useState<string | null>(null);
  const [now, setNow] = useState(Date.now);
  const requestKey = `${source}|${mode}|${region}`;
  const [state, setState] = useState<{ key: string; loading: boolean; report?: MovieDiscoveryReport; error?: MessageKey; playback?: FawesomeShelfPlaybackReport; playbackError?: MessageKey }>({ key: requestKey, loading: true });
  const current = state.key === requestKey ? state : null;
  const loading = current?.loading ?? true;
  const discovery = current?.report;
  const playback = current?.playback;
  const checkedAt = formatDateTime(discovery?.checkedAt);
  const partial = discovery?.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured');
  const playbackItems = new Map(playback?.items.map((movie) => [movie.sourceUrl, movie]) ?? []);
  const movies = (discovery?.items ?? []).map((movie, order) => {
    const progress = playbackItems.get(movie.sourceUrl);
    return { movie: progress ? { ...movie, accessType: progress.accessType, playbackState: progress.playbackState, playbackCheck: progress.playbackCheck, playbackExpiresAt: progress.playbackExpiresAt } : movie, order };
  }).sort((a, b) => Number(shelfPlaybackStarted(b.movie, playback, now)) - Number(shelfPlaybackStarted(a.movie, playback, now)) || a.order - b.order)
    .map(({ movie }) => movie);
  // A progressive promotion must not remove a focused card at the slice edge.
  const focusedIndex = focusedMovieId ? movies.findIndex((movie) => movie.id === focusedMovieId) : -1;
  const displayLimit = Math.min(Math.max(visibleLimit, focusedIndex + 1), 24);
  const startedCount = movies.filter((movie) => shelfPlaybackStarted(movie, playback, now)).length;
  const roundRunning = playback?.state === 'running' && Date.parse(playback.expiresAt) > now && !current?.playbackError;
  const roundExpired = playback?.state === 'expired' || playback?.state === 'running' && Date.parse(playback.expiresAt) <= now;

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    let pollTimer: ReturnType<typeof setTimeout> | undefined;
    let jobId: string | undefined;
    const released = new Set<string>();
    const releaseJob = (id: string) => {
      if (released.has(id)) return;
      released.add(id);
      void fetch(`/api/scout/fawesome/playback?job=${encodeURIComponent(id)}`, { method: 'DELETE', keepalive: true }).catch(() => undefined);
    };
    const request = async (url: string, options?: RequestInit) => {
      const requestController = new AbortController();
      const abort = () => requestController.abort();
      controller.signal.addEventListener('abort', abort, { once: true });
      if (controller.signal.aborted) abort();
      const timeout = setTimeout(abort, 15_000);
      try {
        const response = await fetch(url, { ...options, signal: requestController.signal });
        return { ok: response.ok, data: await response.json() as unknown };
      }
      finally { clearTimeout(timeout); controller.signal.removeEventListener('abort', abort); }
    };
    setVisibleLimit(6);
    setFocusedMovieId(null);
    setNow(Date.now());
    setState({ key: requestKey, loading: true });
    void (async () => {
      try {
        const response = await request(`/api/scout/discover?mode=${mode}&region=${encodeURIComponent(region)}${source === 'fawesome' ? '&source=fawesome' : ''}`);
        if (!response.ok) throw new ShelfError('shelf.error.suggestions');
        const report = response.data as MovieDiscoveryReport;
        if (!Array.isArray(report.items)) throw new ShelfError('shelf.error.suggestions');
        if (source === 'fawesome' && report.items.some((movie) => !isFawesomeMovieSuggestion(movie.sourceUrl))) throw new ShelfError('shelf.error.fawesome');
        if (active) setState({ key: requestKey, loading: false, report });
        if (!active || source !== 'fawesome' || report.items.length === 0) return;
        const catalogUrls = new Set(report.items.map((movie) => movie.sourceUrl));
        const readPlayback = (response: { ok: boolean; data: unknown }): FawesomeShelfPlaybackReport => {
          if (!response.ok) throw new ShelfError('shelf.error.unavailable');
          const snapshot = response.data as FawesomeShelfPlaybackReport;
          if (!/^[a-f0-9]{32}$/.test(snapshot.jobId ?? '') || snapshot.mode !== mode || snapshot.region !== 'ALL'
            || snapshot.adBlockingEnabled !== true || !['running', 'complete', 'cancelled', 'expired'].includes(snapshot.state)
            || !Number.isFinite(Date.parse(snapshot.expiresAt)) || !Array.isArray(snapshot.items)
            || snapshot.items.some((movie) => !isFawesomeMovieSuggestion(movie.sourceUrl) || !catalogUrls.has(movie.sourceUrl))) {
            throw new ShelfError('shelf.error.unreadable');
          }
          return snapshot;
        };
        const acceptPlayback = (snapshot: FawesomeShelfPlaybackReport) => {
          if (!active) { releaseJob(snapshot.jobId); return; }
          setNow(Date.now());
          setState((previous) => previous.key === requestKey ? { ...previous, playback: snapshot, playbackError: undefined } : previous);
        };
        const playbackFailed = () => {
          if (jobId) releaseJob(jobId);
          if (active && !controller.signal.aborted) setState((previous) => previous.key === requestKey
            ? { ...previous, playback: previous.playback ? { ...previous.playback, state: 'cancelled' } : undefined, playbackError: 'shelf.error.stopped' } : previous);
        };
        const pollPlayback = (snapshot: FawesomeShelfPlaybackReport) => {
          const remaining = Date.parse(snapshot.expiresAt) - Date.now();
          if (!active || snapshot.state !== 'running' || remaining <= 0) return;
          const delay = Math.min(Math.max(1_000, Number.isFinite(snapshot.pollAfterMs) ? snapshot.pollAfterMs : 2_000), 5_000, remaining);
          pollTimer = setTimeout(() => {
            if (!active) return;
            if (Date.now() >= Date.parse(snapshot.expiresAt)) { setNow(Date.now()); return; }
            void (async () => {
              try {
                const next = await readPlayback(await request(`/api/scout/fawesome/playback?job=${encodeURIComponent(snapshot.jobId)}`));
                if (next.jobId !== snapshot.jobId) throw new ShelfError('shelf.error.roundChanged');
                acceptPlayback(next);
                pollPlayback(next);
              } catch { playbackFailed(); }
            })();
          }, delay);
        };
        try {
          const snapshot = await readPlayback(await request('/api/scout/fawesome/playback', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode }) }));
          jobId = snapshot.jobId;
          acceptPlayback(snapshot);
          pollPlayback(snapshot);
        } catch { playbackFailed(); }
      } catch (error) {
        if (active && !controller.signal.aborted) setState({ key: requestKey, loading: false, error: shelfFailure(error) });
      }
    })();
    return () => {
      active = false;
      controller.abort();
      if (pollTimer !== undefined) clearTimeout(pollTimer);
      if (jobId) releaseJob(jobId);
    };
  }, [mode, region, requestKey, retry, source]);

  useEffect(() => {
    if (source !== 'fawesome' || !playback) return;
    const expiries = playback.items.map((movie) => Date.parse(movie.playbackExpiresAt ?? ''));
    if (playback.state === 'running') expiries.push(Date.parse(playback.expiresAt));
    const nearest = Math.min(...expiries.filter((expiry) => Number.isFinite(expiry) && expiry > now));
    if (!Number.isFinite(nearest)) return;
    const timer = setTimeout(() => setNow(Date.now()), Math.min(Math.max(1, nearest - Date.now() + 10), 2_147_483_647));
    return () => clearTimeout(timer);
  }, [now, playback, source]);

  return (
    <div className="movie-discovery-shelf" aria-labelledby="movie-discovery-heading">
      <div className="movie-discovery-header">
        <h3 id="movie-discovery-heading">{t('shelf.title')}</h3>
        <div className="preset-chips" role="group" aria-label={t('shelf.choose.group')}>
          {(['popular', 'recent'] as const).map((option) => (
            <button key={option} type="button" className={`chip${source === 'charts' && mode === option ? ' scout-filter-active' : ''}`} aria-pressed={source === 'charts' && mode === option} onClick={() => { setSource('charts'); setMode(option); }}>
              {option === 'popular' ? t('shelf.popularCharts') : t('shelf.recentReleases')}
            </button>
          ))}
          <button type="button" className={`chip${source === 'fawesome' ? ' scout-filter-active' : ''}`} aria-pressed={source === 'fawesome'} onClick={() => { if (source !== 'fawesome') setMode('popular'); setSource('fawesome'); }}>{t('shelf.fawesome')}</button>
        </div>
      </div>
      {source === 'fawesome' && <div className="preset-chips" role="group" aria-label={t('shelf.chooseFawesome')} style={{ marginTop: 10 }}>
        {(['popular', 'recent'] as const).map((option) => <button key={option} type="button" className={`chip${mode === option ? ' scout-filter-active' : ''}`} aria-pressed={mode === option} onClick={() => setMode(option)}>{option === 'popular' ? t('shelf.fawesomePopular') : t('shelf.fawesomeNew')}</button>)}
      </div>}
      <p className="scout-muted">{source === 'fawesome'
        ? t('shelf.note.fawesome')
        : mode === 'popular'
        ? t('shelf.note.popular')
        : t('shelf.note.recent')}</p>
      {loading ? <p className="scout-muted" role="status">{source === 'fawesome' ? t('shelf.loading.fawesome') : t('shelf.loading.charts')}</p> : current?.error ? (
        <div className="movie-discovery-message" role="status"><p>{t(current.error)}</p><button type="button" className="chip" onClick={() => setRetry((attempt) => attempt + 1)}>{t('shelf.retry')}</button></div>
      ) : discovery ? (
        <>
          {partial && <p className="scout-muted" role="status">{t('shelf.partial')}</p>}
          {source === 'fawesome' && discovery.items.length > 0 && <div className="movie-discovery-progress" role="status" aria-live="polite">
            <p>{current?.playbackError ? t(current.playbackError)
              : !playback ? t('shelf.preparing')
              : roundRunning ? t('shelf.progress', { checked: playback.checkedCount, total: playback.total, started: startedCount })
              : roundExpired ? t('shelf.roundEnded', { started: startedCount })
              : playback.state === 'cancelled' ? t('shelf.cancelled')
              : t('shelf.complete', { started: startedCount })}</p>
            {(current?.playbackError || roundExpired) && <button type="button" className="chip" onClick={() => setRetry((attempt) => attempt + 1)}>{t('shelf.checkAgain')}</button>}
          </div>}
          {discovery.items.length > 0 ? (
            <div className="movie-discovery-grid">
              {movies.slice(0, displayLimit).map((movie) => {
                const started = shelfPlaybackStarted(movie, playback, now);
                const sampleAt = formatDateTime(movie.playbackCheck?.checkedAt);
                return <button key={movie.id} type="button" className={`movie-discovery-title${started ? ' movie-discovery-started' : ''}`} onFocus={() => setFocusedMovieId(movie.id)} onBlur={() => setFocusedMovieId(null)} onClick={() => onSelect(movie.query || movie.title)}>
                  {movie.thumbnail && <img src={movie.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer" onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <span>
                    <strong>{movie.title}</strong>
                    <span className="scout-muted">{movie.sourceName}{movie.markets?.length ? ` · ${movie.markets.map((market) => countryName(market, regionName) ?? market).join(', ')}` : ''}</span>
                    {source === 'fawesome' ? movie.catalogDate && <span className="scout-muted">{t('shelf.catalogDate', { date: movie.catalogDate })}</span> : movie.releaseDate && <span className="scout-muted">{t('shelf.storeRelease', { date: movie.releaseDate.slice(0, 10) })}</span>}
                    {source === 'fawesome' && <>
                      <span className={`movie-discovery-playback${started ? ' sample-started' : ''}`}>{shelfPlaybackLabel(movie, playback, now, t)}</span>
                      {sampleAt && <span className="scout-muted">{t('shelf.testLocation', { location: countryName(movie.playbackCheck?.country, regionName) ?? t('card.testLocationUnknown') })} · <time dateTime={movie.playbackCheck?.checkedAt}>{sampleAt}</time></span>}
                      {started && <span className="scout-muted">{t('shelf.shortSample')}</span>}
                    </>}
                    <span className="movie-discovery-action">{t('shelf.findOptions')} <Search size={11} aria-hidden="true" /></span>
                  </span>
                </button>;
              })}
            </div>
          ) : <p className="scout-muted" role="status">{source === 'fawesome' ? t('shelf.noFawesome') : t('shelf.noCharts')}</p>}
          {displayLimit < Math.min(movies.length, 24) && <button type="button" className="chip" style={{ marginTop: 10 }} onClick={() => setVisibleLimit((count) => Math.min(Math.max(count, displayLimit) + 6, 24))}>{t(Math.min(6, Math.min(movies.length, 24) - displayLimit) === 1 ? 'shelf.more.one' : 'shelf.more.other', { count: Math.min(6, Math.min(movies.length, 24) - displayLimit) })}</button>}
          {discovery.detail && <details className="scout-provider-details" style={{ marginTop: 10 }}><summary>{t('shelf.about')}</summary><p>{discovery.detail}</p></details>}
          {checkedAt && <p className="scout-muted">{source === 'fawesome' ? t('shelf.checkedCatalog') : t('shelf.checkedCharts')} <time dateTime={discovery.checkedAt}>{checkedAt}</time>{discovery.cached ? t('shelf.cached') : ''}</p>}
        </>
      ) : null}
    </div>
  );
};

// A recording plays through the relay when its file is free and answered
// the reach check as audio, whether or not the sample ran.
const canPlayAudio = (source: VerifiedSource): boolean =>
  source.accessType === 'free' && Boolean(source.directStreamUrl) && source.verificationStatus === 'media_reachable';

// A confirmed audio sample: a muted beginning decoded and advanced at least
// a second in the isolated browser. Frames and picture are not asked of it.
const audioSampleStarted = (source: VerifiedSource): boolean => {
  const check = source.playbackCheck;
  return source.accessType === 'free' && check?.status === 'started' && check.playbackConfirmed === true
    && check.method === 'isolated_audio_sample' && !check.reason && Number.isFinite(check.secondsAdvanced) && (check.secondsAdvanced ?? 0) >= 1;
};

const audioSampleLabel = (source: VerifiedSource, t: Translate, regionName: RegionFormatter): string => {
  const check = source.playbackCheck;
  if (audioSampleStarted(source)) {
    const location = countryName(check?.country, regionName);
    return location ? t('playback.startedFrom', { location }) : t('playback.started');
  }
  if (!check || check.method === 'not_checked') return t('card.sampleNotTested');
  const reasonKey = check.reason ? PLAYBACK_REASON_KEYS[check.reason] : undefined;
  if (reasonKey) return t(reasonKey);
  switch (check.status) {
    case 'not_started': return t('playback.notStarted');
    case 'unsupported': return t('playback.unsupported');
    case 'skipped': return t('card.sampleNotTested');
    default: return t('card.sampleUnconfirmed');
  }
};

// The stream a track plays as: the chosen file first, the whole set behind
// it so the stage queues the rest in order.
const audioStream = (source: VerifiedSource, track: AudioTrack | undefined, t: Translate): ActiveRelayStream => {
  const collection = source.creator ? `${source.title} · ${source.creator}` : source.title;
  const streamUrl = track?.url ?? (source.directStreamUrl as string);
  const current = track ?? source.tracks?.find((entry) => entry.url === streamUrl);
  const title = current && (source.tracks?.length ?? 0) > 1 ? `${current.title} · ${collection}` : collection;
  return {
    title, collection, streamUrl, pageUrl: source.url, provider: source.provider,
    format: current?.format ? t('stage.directFormat', { format: current.format }) : source.streamFormat, useZeroTrustProxy: true, kind: 'audio', poster: source.thumbnail,
    durationSeconds: current?.durationSeconds, tracks: source.tracks,
  };
};

interface AudioResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionLabel: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The audio results: recordings with their performer, year, length and the
// reach of their file or page. The movie cards' playback and offer rows do
// not apply to a recording yet.
const AudioResults: React.FC<AudioResultsProps> = ({ report, sources, filter, onFilter, regionLabel, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const { t, regionName, formatDateTime } = useI18n();
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{t(sources.length === 1 ? 'search.summary.recordings.one' : 'search.summary.recordings.other', { count: sources.length })}</strong> {t('search.summary.for', { query: report.query })}<span className="scout-muted"> · {regionLabel}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">{t('results.intro.audio')}</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>{t('search.coverage')}{coverageOpen ? t('search.coverage.unavailable') : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? t('search.coverage.ok') : provider.status === 'empty' ? t('search.coverage.empty') : provider.status === 'not_configured' ? t('search.coverage.notConfigured') : t('search.coverage.failed'))}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>{t('search.linkChecks')}</strong> · {t('search.linkChecks.checked.other', { count: report.availabilityChecks.checkedCount })}{checkLocation ? t('search.linkChecks.from', { location: checkLocation }) : t('search.linkChecks.locationUnknown')}</p>
          {report.availabilityChecks.playbackCheckedCount !== undefined && <p><strong>{t('search.sample.mutedAudio')}</strong> · {t('search.sample.attempted.other', { count: report.availabilityChecks.playbackCheckedCount })}{report.availabilityChecks.playbackStartedCount !== undefined ? t('search.sample.started', { count: report.availabilityChecks.playbackStartedCount }) : ''}{report.availabilityChecks.fingerprintCount ? t(report.availabilityChecks.fingerprintCount === 1 ? 'search.sample.audioFingerprints.one' : 'search.sample.audioFingerprints.other', { count: report.availabilityChecks.fingerprintCount }) : ''}{t('search.sample.shortAudio')}</p>}
          {report.atlas?.fingerprints ? <p><strong>{t('search.fingerprintLibrary')}</strong>{t('search.fingerprinted.audio', { count: report.atlas.fingerprints })}</p> : null}
          {batchCheckedAt && <p>{t('search.checked')} <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label={t('search.filters.group')}>
        {FILTERS.map((value) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {t(FILTER_KEYS[value])} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? t('search.empty.audio.noSources') : t('search.filter.noneMarked', { filter: t(FILTER_KEYS[filter]) })}</strong>
          <p>{sources.length === 0 ? t('search.empty.audio.help.noSources') : t('search.filter.otherHint')}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const checkedAt = formatDateTime(check?.checkedAt);
            const length = runtimeSeconds(source.duration);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const sampleStarted = audioSampleStarted(source);
            const sample = source.playbackCheck;
            const sampleCheckedAt = formatDateTime(sample?.checkedAt);
            const reach = sampleStarted ? t('card.reach.samplePassed') : source.verificationStatus === 'media_reachable' ? t('card.reach.audio') : source.verificationStatus === 'page_reachable' ? t('card.reach.page') : t('card.reach.listing');
            const tracks = source.tracks ?? [];
            // The index's verdict, compared with everything it has heard,
            // over the search's own between its two samples.
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const fingerprintNote = sample ? fingerprintLabel(sample, t) : null;
            return (
              <article key={source.id} className="source-card movie-source-card audio-source-card">
                <div>
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{t(ACCESS_KEYS[source.accessType ?? 'unknown'])}</span>
                    <span className={`scout-muted${sampleStarted ? ' scout-media-status' : ''}`}>{reach}</span>
                  </div>
                  <div className="movie-source-title">
                    {source.thumbnail && <img src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                      onError={(event) => { event.currentTarget.hidden = true; }} />}
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">{t('card.performerUnconfirmed')}</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}</p>
                      <p className="scout-muted movie-source-runtime">{length === null ? t('card.lengthUnconfirmed') : t('card.listedLength', { length: formatRuntime(length, t) })}{source.verificationStatus === 'media_reachable' && source.streamFormat ? ` · ${source.streamFormat}` : ''}</p>
                    </div>
                  </div>
                  {source.accessType === 'free' && (
                    <div className={`movie-playback-check${sampleStarted ? ' sample-started' : ''}`}>
                      <p className="movie-check-status">{audioSampleLabel(source, t, regionName)}</p>
                      {sampleStarted
                        ? <p className="scout-muted">{t('card.sampleDecodedAudio', { seconds: (sample?.secondsAdvanced ?? 0).toFixed(1) })}</p>
                        : <p className="scout-muted">{sample?.detail || t('card.noAudioSample')}</p>}
                      {fingerprintNote && <p className="scout-muted">{fingerprintNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity, t)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      {sampleCheckedAt && <p className="scout-muted">{t('card.sampleChecked')} <time dateTime={sample?.checkedAt}>{sampleCheckedAt}</time></p>}
                    </div>
                  )}
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  {tracks.length > 1 && (
                    <details className="scout-provider-details audio-track-list">
                      <summary>{t(tracks.length === 1 ? 'card.tracks.one' : 'card.tracks.other', { count: tracks.length })}</summary>
                      <ol>{tracks.map((track, index) => {
                        const trackLength = track.durationSeconds && track.durationSeconds > 0 ? formatRuntime(track.durationSeconds, t) : null;
                        return (
                          <li key={track.url}>
                            <button type="button" className="btn" disabled={!canPlayAudio(source)} onClick={() => onPlayRelay(audioStream(source, track, t))}>
                              <Play size={12} aria-hidden="true" /> {index + 1}. {track.title}{trackLength ? <span className="scout-muted"> · {trackLength}</span> : null}
                            </button>
                          </li>
                        );
                      })}</ol>
                    </details>
                  )}
                  <div className="movie-source-facts">
                    {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                    <div className="movie-access-check">
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{accessCheckLabel(check, t, regionName)}</p>
                      {check?.detail && <p className="scout-muted">{check.detail}</p>}
                      <p className="scout-muted">{t('card.reachableFileNote')}</p>
                      {checkedAt && <p className="scout-muted">{t('card.linkChecked')} <time dateTime={check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  <button type="button" className="btn btn-primary" disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? t('card.opening') : t('card.openPageProtected')}
                  </button>
                  {canPlayAudio(source) && <button type="button" className="btn" onClick={() => onPlayRelay(audioStream(source, undefined, t))}>
                    <Play size={13} aria-hidden="true" /> {sampleStarted ? (tracks.length > 1 ? t('card.sampledRecordingTracks') : t('card.sampledRecording')) : (tracks.length > 1 ? t('card.allTracks') : t('card.playRelay'))}
                  </button>}
                </div>
              </article>
            );
          })}
        </div>
      )}
    </>
  );
};

// What the isolated sandbox drew of a picture or a document, in a line.
const renderLabel = (check: RenderCheck | undefined, t: Translate): string | null => {
  if (!check || check.status === 'skipped') return null;
  if (check.status === 'rendered') {
    const size = check.width && check.height ? t('render.size', { width: check.width, height: check.height }) : '';
    const took = check.milliseconds ? t('render.took', { seconds: (check.milliseconds / 1000).toFixed(1) }) : '';
    const hash = check.hashAgrees === 'yes' ? t('render.hashYes') : check.hashAgrees === 'no' ? t('render.hashNo') : '';
    const drawn = check.visibleShare ? t('render.visible', { percent: Math.round(check.visibleShare * 100) }) : '';
    const converted = check.converted ? t('render.converted', { format: check.converted }) : '';
    return `${t('render.rendered')}${size}${took}${hash}${drawn}.${converted}`;
  }
  return t('render.failed', { detail: check.detail ?? '' });
};

// A picture opens in the zoomable viewer through the relay whenever its
// file answered, decoded or not: the viewer can show what the probe cannot
// decode yet, such as WebP.
const canViewImage = (source: VerifiedSource): boolean =>
  Boolean(source.directStreamUrl) && (source.imageCheck?.status === 'decoded' || source.imageCheck?.status === 'unsupported');

const imageStream = (source: VerifiedSource, t: Translate): ActiveRelayStream => ({
  title: source.creator ? `${source.title} · ${source.creator}` : source.title,
  streamUrl: source.directStreamUrl as string, pageUrl: source.url, provider: source.provider,
  format: source.streamFormat || (source.imageCheck?.format ? t('image.formatLabel', { format: source.imageCheck.format.toUpperCase() }) : t('kind.image')),
  resolution: source.imageCheck?.width && source.imageCheck?.height ? `${source.imageCheck.width}×${source.imageCheck.height}` : source.resolution,
  useZeroTrustProxy: true, kind: 'image', poster: source.thumbnail,
});

const imageProbeLabel = (check: ImageCheck | undefined, t: Translate): string => {
  switch (check?.status) {
    case 'decoded': return t('image.decoded', { width: check.width ?? '', height: check.height ?? '', format: (check.format ?? '').toUpperCase() }).trim();
    case 'unsupported': return t('image.unsupported', { format: (check.format ?? t('image.probeFormat')).toUpperCase() });
    case 'undecodable': return t('image.undecodable');
    case 'unreachable': return check.httpStatus ? t('image.http', { status: check.httpStatus }) : t('image.unreachable');
    default: return t('image.unprobed');
  }
};

const imageHashLabel = (check: ImageCheck | undefined, t: Translate): string | null => {
  switch (check?.fingerprintStatus) {
    case 'computed': return t('image.hash.computed');
    case 'too_large': return t('image.hash.too_large');
    case 'undecodable': return t('image.hash.undecodable');
    case 'unavailable': return t('image.hash.unavailable');
    default: return null;
  }
};

interface ImageResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionLabel: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The picture results: each picture with its maker, year, dimensions,
// licence, what the probe decoded of its file, and what its hash said
// beside the other pictures of the search and the index.
const ImageResults: React.FC<ImageResultsProps> = ({ report, sources, filter, onFilter, regionLabel, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const { t, regionName, formatDateTime } = useI18n();
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{t(sources.length === 1 ? 'search.summary.pictures.one' : 'search.summary.pictures.other', { count: sources.length })}</strong> {t('search.summary.for', { query: report.query })}<span className="scout-muted"> · {regionLabel}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">{t('results.intro.image')}</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>{t('search.coverage')}{coverageOpen ? t('search.coverage.unavailable') : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? t('search.coverage.ok') : provider.status === 'empty' ? t('search.coverage.empty') : provider.status === 'not_configured' ? t('search.coverage.notConfigured') : t('search.coverage.failed'))}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>{t('results.fileProbes')}</strong> · {t('search.linkChecks.checked.other', { count: report.availabilityChecks.checkedCount })}{checkLocation ? t('search.linkChecks.from', { location: checkLocation }) : t('search.linkChecks.locationUnknown')}{report.availabilityChecks.fingerprintCount ? t(report.availabilityChecks.fingerprintCount === 1 ? 'search.sample.hashes.one' : 'search.sample.hashes.other', { count: report.availabilityChecks.fingerprintCount }) : ''}</p>
          {report.availabilityChecks.renderCheckedCount ? <p><strong>{t('results.sandboxRenders')}</strong> · {t('results.renderAttempted', { count: report.availabilityChecks.renderCheckedCount, rendered: report.availabilityChecks.renderedCount ?? 0 })}</p> : null}
          {report.atlas?.fingerprints ? <p><strong>{t('search.fingerprintLibrary')}</strong>{t('search.fingerprinted.images', { count: report.atlas.fingerprints })}</p> : null}
          {batchCheckedAt && <p>{t('search.checked')} <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label={t('search.filters.group')}>
        {FILTERS.map((value) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {t(FILTER_KEYS[value])} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? t('search.empty.image.noSources') : t('search.filter.noneMarked', { filter: t(FILTER_KEYS[filter]) })}</strong>
          <p>{sources.length === 0 ? t('search.empty.image.help.noSources') : t('search.filter.otherHint')}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const probe = source.imageCheck;
            const checkedAt = formatDateTime(probe?.checkedAt ?? check?.checkedAt);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const hashNote = imageHashLabel(probe, t);
            const dimensions = probe?.width && probe?.height ? `${probe.width}×${probe.height}` : source.resolution ? source.resolution.replace('x', '×') : null;
            return (
              <article key={source.id} className="source-card movie-source-card image-source-card">
                <div>
                  {source.thumbnail && <img className="image-thumb" src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                    onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{t(ACCESS_KEYS[source.accessType ?? 'unknown'])}</span>
                    <span className={`scout-muted${probe?.status === 'decoded' ? ' scout-media-status' : ''}`}>{probe ? imageProbeLabel(probe, t) : source.verificationStatus === 'page_reachable' ? t('check.pageReachable') : t('card.reach.listing')}</span>
                  </div>
                  <div className="movie-source-title">
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">{t('card.makerUnconfirmed')}</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}{dimensions ? ` · ${dimensions}` : ''}{source.license ? ` · ${source.license}` : ''}</p>
                    </div>
                  </div>
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  <div className="movie-source-facts">
                    {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                    <div className="movie-access-check">
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{probe ? imageProbeLabel(probe, t) : accessCheckLabel(check, t, regionName)}</p>
                      {(probe?.detail || check?.detail) && <p className="scout-muted">{probe?.detail || check?.detail}</p>}
                      {hashNote && <p className="scout-muted">{hashNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity, t)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      {renderLabel(source.renderCheck, t) && <p className={`scout-muted${source.renderCheck?.status === 'rendered' ? ' scout-media-status' : ''}`}>{renderLabel(source.renderCheck, t)}</p>}
                      {source.renderCheck?.status !== 'rendered' && <p className="scout-muted">{t('image.headerNote')}</p>}
                      {checkedAt && <p className="scout-muted">{t('card.fileChecked')} <time dateTime={probe?.checkedAt ?? check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  {canViewImage(source) && <button type="button" className="btn btn-primary" onClick={() => onPlayRelay(imageStream(source, t))}>
                    <Scan size={13} aria-hidden="true" /> {t('image.openViewer')}
                  </button>}
                  <button type="button" className={`btn${canViewImage(source) ? '' : ' btn-primary'}`} disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? t('card.opening') : t('card.openPageProtected')}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </>
  );
};

// A document opens in the reader through the relay when its file is a PDF
// that answered, or one the search did not get to probe: the reader checks
// the relay's answer again before framing it. An e-book, a DjVu scan or an
// office file opens in the reader too, which asks the console to convert
// it and says when this host cannot. Plain text opens in the protected
// browser.
const canReadDocument = (source: VerifiedSource): boolean => {
  if (!source.directStreamUrl) return false;
  if (isPdfUrl(source.directStreamUrl)) return source.documentCheck?.status === 'parsed' || source.documentCheck?.status === 'not_checked';
  return isConvertibleUrl(source.directStreamUrl);
};

const documentStream = (source: VerifiedSource, t: Translate): ActiveRelayStream => ({
  title: source.creator ? `${source.title} · ${source.creator}` : source.title,
  streamUrl: source.directStreamUrl as string, pageUrl: source.url, provider: source.provider,
  format: source.streamFormat || (source.documentCheck?.format ? t('document.formatLabel', { format: source.documentCheck.format.toUpperCase() }) : t('document.fallback')),
  useZeroTrustProxy: true, kind: 'document', poster: source.thumbnail,
});

const documentProbeLabel = (check: DocumentCheck | undefined, t: Translate): string => {
  switch (check?.status) {
    case 'parsed': {
      const format = (check.format ?? '').toUpperCase();
      if (check.pages) return `${t(check.pages === 1 ? 'document.parsedPages.one' : 'document.parsedPages.other', { format, count: check.pages })}${check.pagesConfirmed ? '' : t('document.hostCount')}`;
      return t('document.parsed', { format }).trim();
    }
    case 'unsupported': return t('document.unsupported', { format: (check.format ?? t('document.this')).toUpperCase() });
    case 'undecodable': return t('document.undecodable');
    case 'unreachable': return check.httpStatus ? t('document.http', { status: check.httpStatus }) : t('document.unreachable');
    default: return t('document.unprobed');
  }
};

const documentFingerprintLabel = (check: DocumentCheck | undefined, t: Translate, formatNumber: (value: number) => string): string | null => {
  switch (check?.fingerprintStatus) {
    case 'computed': {
      const from = t(check.textSource === 'ocr_text' ? 'document.fingerprint.ocr' : check.textSource === 'text_file' ? 'document.fingerprint.textFile' : 'document.fingerprint.pdf');
      const body = check.bodyWords ? `${t('document.fingerprint.body', { words: formatNumber(check.bodyWords) })}${check.bodySkipped ? t('document.fingerprint.skipped', { words: formatNumber(check.bodySkipped) }) : ''}` : '';
      return t('document.fingerprint.computed', { source: from, body });
    }
    case 'too_short': return t('document.fingerprint.too_short');
    case 'unreadable': return t('document.fingerprint.unreadable');
    case 'partial': return t('document.fingerprint.partial', { words: formatNumber(check.bodyWords ?? 0) });
    default: return null;
  }
};

interface DocumentResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionLabel: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The document results: each document with its author, year, page count
// and collection, what the probe read of its file, and what its text
// fingerprint said beside the other documents of the search and the index.
const DocumentResults: React.FC<DocumentResultsProps> = ({ report, sources, filter, onFilter, regionLabel, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const { t, regionName, formatDateTime, formatNumber } = useI18n();
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{t(sources.length === 1 ? 'search.summary.documents.one' : 'search.summary.documents.other', { count: sources.length })}</strong> {t('search.summary.for', { query: report.query })}<span className="scout-muted"> · {regionLabel}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">{t('results.intro.document')}</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>{t('search.coverage')}{coverageOpen ? t('search.coverage.unavailable') : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? t('search.coverage.ok') : provider.status === 'empty' ? t('search.coverage.empty') : provider.status === 'not_configured' ? t('search.coverage.notConfigured') : t('search.coverage.failed'))}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>{t('results.fileProbes')}</strong> · {t('search.linkChecks.checked.other', { count: report.availabilityChecks.checkedCount })}{checkLocation ? t('search.linkChecks.from', { location: checkLocation }) : t('search.linkChecks.locationUnknown')}{report.availabilityChecks.fingerprintCount ? t(report.availabilityChecks.fingerprintCount === 1 ? 'search.sample.textFingerprints.one' : 'search.sample.textFingerprints.other', { count: report.availabilityChecks.fingerprintCount }) : ''}</p>
          {report.availabilityChecks.renderCheckedCount ? <p><strong>{t('results.sandboxRenders')}</strong> · {t('results.renderAttemptedDocument', { count: report.availabilityChecks.renderCheckedCount, rendered: report.availabilityChecks.renderedCount ?? 0 })}</p> : null}
          {report.atlas?.fingerprints ? <p><strong>{t('search.fingerprintLibrary')}</strong>{t('search.fingerprinted.documents', { count: report.atlas.fingerprints })}</p> : null}
          {batchCheckedAt && <p>{t('search.checked')} <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label={t('search.filters.group')}>
        {FILTERS.map((value) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {t(FILTER_KEYS[value])} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? t('search.empty.document.noSources') : t('search.filter.noneMarked', { filter: t(FILTER_KEYS[filter]) })}</strong>
          <p>{sources.length === 0 ? t('search.empty.document.help.noSources') : t('search.filter.otherHint')}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const probe = source.documentCheck;
            const checkedAt = formatDateTime(probe?.checkedAt ?? check?.checkedAt);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const fingerprintNote = documentFingerprintLabel(probe, t, formatNumber);
            const pages = probe?.pages || source.pages;
            return (
              <article key={source.id} className="source-card movie-source-card image-source-card document-source-card">
                <div>
                  {source.thumbnail && <img className="image-thumb" src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                    onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{t(ACCESS_KEYS[source.accessType ?? 'unknown'])}</span>
                    <span className={`scout-muted${probe?.status === 'parsed' ? ' scout-media-status' : ''}`}>{probe ? documentProbeLabel(probe, t) : source.verificationStatus === 'page_reachable' ? t('check.pageReachable') : t('card.reach.listing')}</span>
                  </div>
                  <div className="movie-source-title">
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">{t('card.authorUnconfirmed')}</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}{pages ? ` · ${t(pages === 1 ? 'document.pages.one' : 'document.pages.other', { count: pages })}` : ''}{source.license ? ` · ${source.license}` : ''}</p>
                    </div>
                  </div>
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  <div className="movie-source-facts">
                    {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                    <div className="movie-access-check">
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{probe ? documentProbeLabel(probe, t) : accessCheckLabel(check, t, regionName)}</p>
                      {(probe?.detail || check?.detail) && <p className="scout-muted">{probe?.detail || check?.detail}</p>}
                      {fingerprintNote && <p className="scout-muted">{fingerprintNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity, t)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      {renderLabel(source.renderCheck, t) && <p className={`scout-muted${source.renderCheck?.status === 'rendered' ? ' scout-media-status' : ''}`}>{renderLabel(source.renderCheck, t)}</p>}
                      {source.renderCheck?.status !== 'rendered' && <p className="scout-muted">{t('document.parsedNote')}</p>}
                      {checkedAt && <p className="scout-muted">{t('card.fileChecked')} <time dateTime={probe?.checkedAt ?? check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  {canReadDocument(source) && <button type="button" className="btn btn-primary" onClick={() => onPlayRelay(documentStream(source, t))}>
                    <BookOpen size={13} aria-hidden="true" /> {t('document.openReader')}
                  </button>}
                  <button type="button" className={`btn${canReadDocument(source) ? '' : ' btn-primary'}`} disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? t('card.opening') : t('card.openPageProtected')}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </>
  );
};

export const SearchScoutPanel: React.FC<SearchScoutPanelProps> = ({
  report, known, merged, searching, searchError, launchError, launchingUrl, onSearch,
  onRunBenchmark, onLaunchRbi, onPlayRelay,
}) => {
  const { t, regionName, formatDateTime, formatNumber } = useI18n();
  const [query, setQuery] = useState('');
  const [region, setRegion] = useState('ALL');
  const [fullLengthOnly, setFullLengthOnly] = useState(true);
  const [kind, setKind] = useState<SearchKind>('video');
  const [filter, setFilter] = useState<AccessFilter>('all');
  const [playbackOnly, setPlaybackOnly] = useState(false);
  const sources = (report?.sources ?? report?.verifiedSources ?? []).filter((source) => source.destinationType !== 'guide');
  const sampledSources = playbackOnly ? sources.filter(playbackSampleStarted) : sources;
  const visibleSources = sampledSources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const reportRegion = report?.region ?? region;
  const reportRegionLabel = reportRegion === 'ALL' ? t('search.region.worldwide') : countryName(reportRegion, regionName) ?? reportRegion;
  const selectedRegionLabel = region === 'ALL' ? t('search.region.worldwide') : countryName(region, regionName) ?? region;
  const allAccessUnconfirmed = sources.length > 0 && sources.every((source) => accessGroup(source) === 'unknown');
  const checkLocation = countryName(report?.availabilityChecks?.country, regionName);
  const batchCheckedAt = formatDateTime(report?.availabilityChecks?.checkedAt);

  const search = (nextQuery: string) => {
    if (!nextQuery.trim()) return;
    setFilter('all');
    onSearch(nextQuery.trim(), region, fullLengthOnly, kind);
  };

  const handleSelectRelay = (source: VerifiedSource) => {
    const streamUrl = source.directStreamUrl || source.detectedManifests?.[0];
    if (!canTryFreeMedia(source) || !streamUrl) return;
    onPlayRelay({
      title: source.title, streamUrl, pageUrl: source.url, provider: source.provider,
      format: source.streamFormat, resolution: source.resolution, useZeroTrustProxy: true,
      kind: 'video', poster: source.thumbnail,
    });
  };

  return (
    <section className="scout-panel" aria-labelledby="movie-search-heading" aria-busy={searching}>
      <div className="movie-search-heading">
        <div>
          <h2 id="movie-search-heading">{kind === 'audio' ? t('search.heading.audio') : kind === 'image' ? t('search.heading.image') : kind === 'document' ? t('search.heading.document') : t('search.heading.video')}</h2>
          <p className="scout-description">{kind === 'audio'
            ? t('search.intro.audio')
            : kind === 'image'
              ? t('search.intro.image')
              : kind === 'document'
                ? t('search.intro.document')
                : region === 'ALL'
              ? t('search.intro.video.worldwide')
              : t('search.intro.video.region', { region: selectedRegionLabel })}</p>
        </div>
        <button type="button" className="btn btn-theater scout-benchmark" disabled={searching}
          onClick={onRunBenchmark} title={t('search.benchmark.title')}>
          <Sparkles size={13} aria-hidden="true" /> {t('search.benchmark')}
        </button>
      </div>

      <div className="preset-chips movie-kind-toggle" role="group" aria-label={t('search.kind.group')}>
        <button type="button" className={`chip${kind === 'video' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'video'} onClick={() => setKind('video')}>
          <Film size={12} aria-hidden="true" /> {t('search.kind.video')}
        </button>
        <button type="button" className={`chip${kind === 'audio' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'audio'} onClick={() => setKind('audio')}>
          <Music size={12} aria-hidden="true" /> {t('search.kind.audio')}
        </button>
        <button type="button" className={`chip${kind === 'image' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'image'} onClick={() => setKind('image')}>
          <ImageIcon size={12} aria-hidden="true" /> {t('search.kind.image')}
        </button>
        <button type="button" className={`chip${kind === 'document' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'document'} onClick={() => setKind('document')}>
          <FileText size={12} aria-hidden="true" /> {t('search.kind.document')}
        </button>
      </div>

      <form className="search-row movie-search-form" onSubmit={(event) => { event.preventDefault(); search(query); }}>
        <label className="movie-query-field">
          <span className="scout-field-label">{kind === 'audio' ? t('search.field.audio') : kind === 'image' ? t('search.field.image') : kind === 'document' ? t('search.field.document') : t('search.field.video')}</span>
          <input className="search-input" value={query} onChange={(event) => setQuery(event.target.value)}
            placeholder={kind === 'audio' ? t('search.placeholder.audio') : kind === 'image' ? t('search.placeholder.image') : kind === 'document' ? t('search.placeholder.document') : t('search.placeholder.video')} maxLength={500} required />
        </label>
        <label className="movie-region-field">
          <span className="scout-field-label">{t('search.region')}</span>
          <select className="search-input" value={region} onChange={(event) => setRegion(event.target.value)}>
            {REGIONS.map(([code, label]) => <option key={code} value={code}>{code === 'ALL' ? t('search.region.worldwide') : countryName(code, regionName) ?? label}</option>)}
          </select>
        </label>
        <button type="submit" className="btn btn-primary movie-search-submit" disabled={!query.trim()}>
          <Search size={14} aria-hidden="true" /> {searching ? t('search.submit.again') : kind === 'audio' ? t('search.submit.audio') : kind === 'image' ? t('search.submit.image') : kind === 'document' ? t('search.submit.document') : t('search.submit.video')}
        </button>
        {kind === 'video' && (
          <label className="movie-length-preference">
            <input type="checkbox" checked={fullLengthOnly} onChange={(event) => setFullLengthOnly(event.target.checked)}
              aria-describedby="movie-length-help" />
            <span><strong>{t('search.fullLength')}</strong><span id="movie-length-help" className="scout-muted">{t('search.fullLength.help')}</span></span>
          </label>
        )}
      </form>

      {kind === 'video' ? (
        <>
          <MovieDiscoveryShelf region={region} onSelect={(title) => { setQuery(title); search(title); }} />
          <div>
            <div className="preset-chips" role="group" aria-label={t('search.examples.group')}>
              <span className="scout-muted">{t('search.examples.label')}</span>
              {FREE_CATALOG_EXAMPLES.map((title) => <button key={title} type="button" className="chip" onClick={() => { setQuery(title); search(title); }}>{title}</button>)}
            </div>
            <p className="scout-muted">{t('search.examples.note')}</p>
          </div>
        </>
      ) : kind === 'audio' ? (
        <p className="scout-muted">{t('search.note.audio')}</p>
      ) : kind === 'image' ? (
        <p className="scout-muted">{t('search.note.image')}</p>
      ) : (
        <p className="scout-muted">{t('search.note.document')}</p>
      )}
      <details className="scout-provider-details format-matrix-details">
        <summary>{t('search.formats.summary')}</summary>
        <FormatMatrix />
      </details>
      <p className="scout-protection-note"><Shield size={14} aria-hidden="true" /> {t('search.protection')}</p>
      {kind === 'video' && <p className="scout-muted">{t('search.nativeSamples')}</p>}

      {searchError && <p className="scout-error" role="alert">{searchError}</p>}
      {launchError && <p className="scout-error" role="alert">{launchError}</p>}
      {searching && <p className="scout-empty" role="status">{kind === 'audio' ? t('search.status.audio') : kind === 'image' ? t('search.status.image') : kind === 'document' ? t('search.status.document') : t('search.status.video')}</p>}

      {(report || searching) && <IndexResolution query={report?.query ?? query} />}
      <RankedLocations known={known} merged={merged} report={report} searching={searching} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} />

      {report && !searching && !searchError && report.kind === 'audio' && (
        <AudioResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionLabel={reportRegionLabel}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind === 'image' && (
        <ImageResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionLabel={reportRegionLabel}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind === 'document' && (
        <DocumentResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionLabel={reportRegionLabel}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind !== 'audio' && report.kind !== 'image' && report.kind !== 'document' && (
        <>
          <div className="movie-results-summary" role="status">
            <div><strong>{t(sources.length === 1 ? 'search.summary.moviePages.one' : 'search.summary.moviePages.other', { count: sources.length })}</strong> {t('search.summary.for', { query: report.query })}<span className="scout-muted"> · {reportRegionLabel}</span></div>
            <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
          </div>
          {report.fullLengthOnly === true && <p className="scout-muted">{t('search.fullLengthNote')}</p>}
          {report.region === 'ALL' && <p className="scout-muted">{t('search.worldwideNote')}</p>}
          {report.providers && report.providers.length > 0 && (
            <details className="scout-provider-details" open={report.providers.some((provider) => provider.status === 'error' || provider.status === 'not_configured')}>
              <summary>{t('search.coverage')}{report.providers.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ? t('search.coverage.unavailable') : ''}</summary>
              <ul>{report.providers.map((provider) => (
                <li key={provider.name}>
                  <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? t('search.coverage.ok') : provider.status === 'empty' ? t('search.coverage.empty') : provider.status === 'not_configured' ? t('search.coverage.notConfigured') : t('search.coverage.failed'))}
                </li>
              ))}</ul>
            </details>
          )}
          {report.availabilityChecks && (
            <div className="scout-link-check-summary">
              <p><strong>{t('search.linkChecks')}</strong> · {t('search.linkChecks.checked.other', { count: report.availabilityChecks.checkedCount })}{checkLocation ? t('search.linkChecks.from', { location: checkLocation }) : t('search.linkChecks.locationUnknown')}</p>
              {report.availabilityChecks.playbackCheckedCount !== undefined && <p><strong>{t('search.sample.freePlayback')}</strong> · {t('search.sample.attempted.other', { count: report.availabilityChecks.playbackCheckedCount })}{report.availabilityChecks.playbackStartedCount !== undefined ? t('search.sample.started', { count: report.availabilityChecks.playbackStartedCount }) : ''}{t('search.sample.shortMovie')}</p>}
              {(report.availabilityChecks.providerCheckedCount ?? 0) > 0 && <p><strong>{t('search.sample.providerChecks')}</strong> · {report.availabilityChecks.providerCheckedCount}{t('search.sample.providerChecksNote')}</p>}
              {reportRegion === 'ALL' && <p>{checkLocation ? t('search.playbackFrom', { location: checkLocation }) : t('search.playbackLocationUnknown')}{t('search.playbackElsewhere')}</p>}
              {report.availabilityChecks.detail && <p>{report.availabilityChecks.detail}</p>}
              {batchCheckedAt && <p>{t('search.checked')} <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
            </div>
          )}
          <div className="preset-chips" role="group" aria-label={t('search.filters.group')}>
            {FILTERS.map((value) => (
              <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
                aria-pressed={filter === value} onClick={() => setFilter(value)}>
                {t(FILTER_KEYS[value])} ({value === 'all' ? sampledSources.length : sampledSources.filter((source) => accessGroup(source) === value).length})
              </button>
            ))}
          </div>
          <label className="movie-length-preference">
            <input type="checkbox" checked={playbackOnly} onChange={(event) => setPlaybackOnly(event.target.checked)} aria-describedby="movie-playback-filter-help" />
            <span><strong>{t('search.playbackOnly')}</strong><span id="movie-playback-filter-help" className="scout-muted">{t('search.playbackOnly.help')}</span></span>
          </label>
          {allAccessUnconfirmed && <p className="scout-muted">{t('search.allUnconfirmed')}</p>}

          {visibleSources.length === 0 ? (
            <div className="scout-empty" role="status">
              <strong>{sources.length === 0
                ? (reportRegion === 'ALL' ? t('search.empty.video.worldwide') : t('search.empty.video.region', { region: reportRegionLabel }))
                : playbackOnly ? t('search.empty.video.playbackOnly')
                : filter === 'unknown' ? t('search.empty.video.unknown')
                : t('search.empty.video.filtered', { filter: t(FILTER_KEYS[filter]) })}</strong>
              <p>{sources.length === 0
                ? t('search.empty.video.help.noSources', { fullLength: report.fullLengthOnly ? t('search.empty.video.help.noSourcesFullLength') : t('search.empty.video.help.noSourcesRegion') })
                : playbackOnly ? t('search.empty.video.help.playbackOnly')
                : t('search.empty.video.help.other')}</p>
            </div>
          ) : (
            <div className="sources-grid movie-sources-grid">
              {visibleSources.map((source) => {
                const check = source.availabilityCheck;
                const checkedAt = formatDateTime(check?.checkedAt);
                const checkCountry = countryName(check?.country, regionName);
                const listingMarket = marketCode(source.region);
                const playback = source.playbackCheck;
                const movieIdentity = report.atlas?.identities?.[source.url] ?? source.identity;
                const movieFingerprintNote = playback ? fingerprintLabel(playback, t) : null;
                const providerPreflight = playback?.method === 'provider_playback_preflight';
                const sampleStarted = playbackSampleStarted(source);
                const sampleCountry = countryName(playback?.country, regionName);
                const sampleMatchesRegion = Boolean(sampleStarted && (reportRegion === 'ALL' || (sampleCountry && marketCode(playback?.country) === reportRegion)));
                const regionHeadline = sampleStarted
                  ? reportRegion === 'ALL' ? t('card.reach.samplePassed') : sampleCountry
                    ? sampleMatchesRegion ? t('playback.startedFrom', { location: sampleCountry }) : t('availability.sampleElsewhere', { market: sampleCountry, country: reportRegionLabel })
                    : t('playback.startedLocationUnknown')
                  : availabilityLabel(source, reportRegion, reportRegionLabel, t, regionName);
                const sampleCheckedAt = formatDateTime(playback?.checkedAt);
                const sampleMetrics = playbackMetrics(source, t, formatNumber);
                const sampleDuration = playback?.method === 'isolated_video_sample' ? playback.durationSeconds : undefined;
                const measuredRuntime = typeof sampleDuration === 'number' && Number.isFinite(sampleDuration) && sampleDuration > 0 ? sampleDuration : null;
                const listedRuntime = runtimeSeconds(source.duration);
                const movieRuntime = measuredRuntime ?? listedRuntime;
                const showReleaseYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
                const description = source.id.startsWith('ref-') && source.verificationStatus !== 'media_reachable'
                  ? t('card.referenceEntry')
                  : source.description;
                const canRelay = canTryFreeMedia(source);
                const evidenceLinks = [
                  { label: t('card.evidence.access'), url: source.accessEvidenceUrl },
                  { label: t('card.evidence.regional'), url: source.availabilityEvidenceUrl },
                  { label: t('card.evidence.page'), url: check?.evidenceUrl },
                  { label: t('card.evidence.playback'), url: playback?.evidenceUrl },
                ].filter((evidence): evidence is { label: string; url: string } => Boolean(evidence.url && evidence.url !== source.url))
                  .filter((evidence, index, entries) => entries.findIndex((entry) => entry.url === evidence.url) === index);
                return (
                  <article key={source.id} className="source-card movie-source-card">
                    <div>
                      <div className="movie-source-badges">
                        <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{t(ACCESS_KEYS[source.accessType ?? 'unknown'])}</span>
                        <span className={`scout-muted${sampleMatchesRegion ? ' scout-media-status' : ''}`}>{regionHeadline}</span>
                      </div>
                      <div className="movie-source-title">
                        {source.thumbnail && <img src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                          onError={(event) => { event.currentTarget.hidden = true; }} />}
                        <div>
                          <h3>{source.title}{showReleaseYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                          <p className="movie-source-provider">{source.provider}</p>
                          <p className="scout-muted movie-source-host">{sourceHost(source.url)}</p>
                          <p className="scout-muted movie-source-runtime">{movieRuntime === null ? t('card.lengthUnconfirmed') : `${t(measuredRuntime !== null ? 'card.mediaRuntime' : 'card.listedRuntime', { runtime: formatRuntime(movieRuntime, t) })} · ${t(movieRuntime >= 40 * 60 ? measuredRuntime !== null ? 'card.featureLengthMedia' : 'card.featureLengthListing' : 'card.shortFilm')}`}</p>
                          {source.provider === 'Fawesome' && !source.releaseYear && <p className="scout-muted">{t('card.originalYear')}</p>}
                        </div>
                      </div>
                      <div className={`movie-playback-check${sampleStarted ? ' sample-started' : ''}`}>
                        <p className="movie-check-status">{playbackCheckLabel(source, reportRegion, t, regionName)}</p>
                        {sampleStarted ? <p className="scout-muted">{t('card.shortSample')}</p> : (
                          <p className="scout-muted">{source.accessType !== 'free'
                            ? t('card.paidNotTested')
                            : playback?.detail || t('card.noSample')}</p>
                        )}
                        {providerPreflight && <p className="scout-muted">{t('card.providerPreflightNote')}</p>}
                        {sampleMetrics && <p className="scout-muted">{sampleMetrics}</p>}
                        {movieFingerprintNote && <p className="scout-muted">{movieFingerprintNote}</p>}
                        {movieIdentity && (
                          <p className={`audio-identity identity-${movieIdentity.effect ?? movieIdentity.verdict ?? 'none'}`}>
                            <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(movieIdentity, t)}</span>
                            {movieIdentity.detail && <span className="scout-muted">{movieIdentity.detail}</span>}
                          </p>
                        )}
                        {playback && (reportRegion === 'ALL' || !sampleStarted) && source.accessType === 'free' && <p className="scout-muted">{t('card.testLocation', { location: sampleCountry ?? t('card.testLocationUnknown') })}</p>}
                        {sampleCheckedAt && <p className="scout-muted">{providerPreflight ? t('card.providerChecked') : t('card.playbackChecked')} <time dateTime={playback?.checkedAt}>{sampleCheckedAt}</time></p>}
                      </div>
                      {description && <p className="scout-description movie-source-description">{description}</p>}
                      <div className="movie-source-facts">
                        <p>{accessSummary(source, t)}</p>
                        {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                        {listingMarket && <p className="scout-muted">{t('card.listingMarket', { market: listingMarket })}</p>}
                        {source.availabilityNote && <p className="scout-muted">{source.availabilityNote}</p>}
                        <div className="movie-access-check">
                          <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{accessCheckLabel(check, t, regionName)}</p>
                          {check?.detail && <p className="scout-muted">{check.detail}</p>}
                          {check && !['page_reachable', 'media_reachable'].includes(check.status) && <p className="scout-muted">{t('card.checkLocation', { location: checkCountry ?? t('card.testLocationUnknown') })}</p>}
                          {!check && source.verificationStatus === 'media_reachable' && <p className="scout-muted">{t('card.reachableDuringDiscovery')}</p>}
                          <p className="scout-muted">{t('card.reachableNoPlayback')}</p>
                          {checkedAt && <p className="scout-muted">{t('card.linkChecked')} <time dateTime={check?.checkedAt}>{checkedAt}</time></p>}
                        </div>
                      </div>
                      {evidenceLinks.length > 0 && (
                        <details className="scout-provider-details movie-source-evidence">
                          <summary>{t('card.evidence')}</summary>
                          <div className="movie-evidence-actions">{evidenceLinks.map((evidence) => (
                            <button key={evidence.url} type="button" className="btn" disabled={launchingUrl !== null}
                              onClick={() => onLaunchRbi(evidence.url, false)}>
                              <Shield size={12} aria-hidden="true" /> {evidence.label}
                            </button>
                          ))}</div>
                        </details>
                      )}
                    </div>
                    <div className="movie-source-actions">
                      <button type="button" className="btn btn-primary" disabled={launchingUrl !== null}
                        onClick={() => onLaunchRbi(source.url, false)}>
                        <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? t('card.opening') : t('card.openMovieProtected')}
                      </button>
                      {canRelay && <button type="button" className="btn" onClick={() => handleSelectRelay(source)}>
                        <Play size={13} aria-hidden="true" /> {sampleStarted ? t('card.playSampledMovie') : t('card.tryFreeMovie')}
                      </button>}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          {(report.prunedSources?.length ?? 0) > 0 && (
            <details className="scout-provider-details">
              <summary>{t(report.prunedSources.length === 1 ? 'card.excluded.one' : 'card.excluded.other', { count: report.prunedSources.length })}</summary>
              <ul>{report.prunedSources.map((source) => <li key={source.id}><strong>{source.title}</strong>: {source.prunedReason || t('card.excluded.default')}</li>)}</ul>
            </details>
          )}
        </>
      )}
    </section>
  );
};
