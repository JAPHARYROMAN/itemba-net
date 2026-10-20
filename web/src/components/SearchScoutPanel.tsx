import React, { useEffect, useState } from 'react';
import { Play, Globe, Search, Shield, Clock, Sparkles, Film, Music, Fingerprint, Image as ImageIcon, Scan, FileText, BookOpen } from 'lucide-react';
import { ActiveRelayStream, AudioTrack, DocumentCheck, FawesomeShelfPlaybackReport, ImageCheck, MovieAvailabilityCheck, MovieDiscoveryItem, MovieDiscoveryMode, MovieDiscoveryReport, SearchKind, SearchKnownEvent, SearchMergedEvent, SearchReport, VerifiedSource } from '../types';
import { RankedLocations } from './RankedLocations';
import { fingerprintLabel, identityLabel } from '../identity';
import { isPdfUrl } from '../media/media';

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

const REGIONS = [
  ['ALL', 'Worldwide'], ['US', 'United States'], ['GB', 'United Kingdom'],
  ['IN', 'India'], ['ZA', 'South Africa'], ['KE', 'Kenya'], ['TZ', 'Tanzania'],
  ['CA', 'Canada'], ['AU', 'Australia'], ['DE', 'Germany'], ['FR', 'France'],
];
const COUNTRY_NAMES = typeof Intl.DisplayNames === 'function' ? new Intl.DisplayNames(['en'], { type: 'region' }) : null;
const FREE_CATALOG_EXAMPLES = ['Seder-Masochism 2018', 'Planetary Defenders 2025', 'The Secret Wife 2024'];
const PROVIDER_MOVIE_BASES = new Set(['filmzie_movie_metadata', 'nasa_movie_metadata', 'creator_movie_metadata', 'fawesome_movie_metadata', 'nfb_movie_metadata']);
const ACCESS_LABELS = {
  free: 'Free', subscription: 'Paid · Subscription', rent: 'Paid · Rent', buy: 'Paid · Buy', paid: 'Paid', unknown: 'Access unconfirmed',
};
const PLAYBACK_REASON_LABELS = {
  region_unavailable: 'Provider unavailable at test location',
  sign_in_required: 'Sign-in required to check playback',
  drm_required: 'DRM playback not tested',
  content_unconfirmed: 'Main movie playback unconfirmed',
  trailer_only: 'Trailer found · movie unconfirmed',
  blocked_with_protection: 'Playback unconfirmed with ad filtering',
  timeout: 'Playback check timed out',
  ads_only: 'Ad stream found · movie unconfirmed',
  expired: 'Playback check expired',
  protection_changed: 'Playback unconfirmed · protection changed',
  location_changed: 'Playback unconfirmed · test location changed',
};
type AccessFilter = 'all' | 'free' | 'paid' | 'unknown';
const FILTERS: { value: AccessFilter; label: string }[] = [
  { value: 'all', label: 'All' }, { value: 'free', label: 'Free' },
  { value: 'paid', label: 'Paid' }, { value: 'unknown', label: 'Unknown' },
];

const accessGroup = (source: VerifiedSource): AccessFilter => {
  if (source.accessType === 'free') return 'free';
  if (source.accessType && source.accessType !== 'unknown') return 'paid';
  return 'unknown';
};

const marketCode = (market?: string): string | null => {
  const code = market?.trim().toUpperCase();
  return code && /^[A-Z]{2}$/.test(code) ? code : null;
};

const countryName = (country?: string): string | null => {
  const code = marketCode(country);
  if (!code) return null;
  const name = COUNTRY_NAMES?.of(code) ?? REGIONS.find(([region]) => region === code)?.[1];
  return name && name !== code && name !== 'Unknown Region' ? name : null;
};

const accessSummary = (source: VerifiedSource): string => {
  if (source.accessType === 'free' && PROVIDER_MOVIE_BASES.has(source.accessBasis ?? '')) return 'Provider identified the full movie; playback is checked separately.';
  if (source.accessType === 'free' && (source.accessBasis === 'archive_metadata' || source.accessBasis === 'peertube_api')) return 'Free catalog video identified; playback and location are checked separately.';
  if (source.accessBasis === 'creator_catalog') return 'The official creator lists a free full feature; current playback is checked separately.';
  if (source.accessBasis === 'provider_model') return "Service pricing; this movie's offer unconfirmed.";
  if (source.accessBasis === 'catalog' && source.price) return source.price;
  if (accessGroup(source) === 'unknown') return "This movie's price and access are unconfirmed.";
  if (source.accessBasis === 'catalog') return source.accessType === 'free' ? 'Free catalog listing; availability may vary.' : 'Catalog listing; current price not supplied.';
  if (source.accessBasis === 'page_text') return source.accessType === 'free' ? 'Page indicates free access; confirm on the movie page.' : 'Page indicates paid access; confirm the current offer.';
  return source.accessType === 'free' ? 'Listed as free; current movie offer unconfirmed.' : 'Price and current movie offer unconfirmed.';
};

const availabilityLabel = (source: VerifiedSource, country: string, countryName: string): string => {
  const market = marketCode(source.region);
  if (country === 'ALL') {
    switch (source.availabilityStatus) {
      case 'regional_listing': return market ? `Listed in ${REGIONS.find(([code]) => code === market)?.[1] ?? market}` : 'Regional listing';
      case 'service_available': return 'Service supported · movie unconfirmed';
      case 'other_region': return market ? `${market} offer · availability varies` : 'Other market · availability unconfirmed';
      case 'unavailable': return 'Service availability restricted';
      default: return 'Regional availability unconfirmed';
    }
  }
  switch (source.availabilityStatus) {
    case 'regional_listing': return market && market !== country ? `${market} offer · ${countryName} unconfirmed` : `Listed in ${countryName}`;
    case 'service_available': return `Service in ${countryName} · movie unconfirmed`;
    case 'other_region': return `${market || 'Other country'} offer · ${countryName} unconfirmed`;
    case 'unavailable': return `Not supported in ${countryName}`;
    default: return `${countryName} availability unconfirmed`;
  }
};

const sourceHost = (url: string): string => {
  try { return new URL(url).hostname.replace(/^www\./, ''); } catch { return url; }
};

const formatCheckedAt = (timestamp?: string): string | null => {
  if (!timestamp) return null;
  const checked = new Date(timestamp);
  return Number.isNaN(checked.getTime()) ? null : checked.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
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

const formatRuntime = (seconds: number): string => {
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  return hours > 0 ? `${hours}h ${minutes % 60}m` : minutes > 0 ? `${minutes}m` : `${Math.floor(seconds)}s`;
};

const accessCheckLabel = (check?: MovieAvailabilityCheck): string => {
	if (check?.method === 'not_checked') return 'Unchecked';
  const location = countryName(check?.country);
  const checkedFrom = location ? ` from ${location}` : ' · check location unconfirmed';
  switch (check?.status) {
    case 'media_reachable': return `Media URL reachable${checkedFrom}`;
    case 'page_reachable': return `Page reachable${checkedFrom}`;
    case 'region_blocked': return 'Regional block detected';
    case 'not_found': return 'Link not found';
    case 'sign_in_required': return 'Sign-in needed';
    case 'access_restricted': return 'Access check blocked';
    case 'unconfirmed': return 'Link check inconclusive';
    case 'error': return 'Access check failed';
    default: return 'Unchecked';
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

const playbackCheckLabel = (source: VerifiedSource, region: string): string => {
  if (source.accessType !== 'free') return 'Playback test skipped';
  const check = source.playbackCheck;
  if (playbackSampleStarted(source)) {
    if (region === 'ALL') return 'Playback started';
    const location = countryName(check?.country);
    return location ? `Playback started from ${location}` : 'Playback started · check location unconfirmed';
  }
  const reasonLabel = check?.reason ? PLAYBACK_REASON_LABELS[check.reason] : undefined;
  if (reasonLabel) return reasonLabel;
  if (check?.method === 'provider_playback_preflight') return 'Provider checked · movie playback unconfirmed';
  switch (check?.status) {
    case 'not_started': return 'Playback did not start';
    case 'unsupported': return 'Playback test unsupported';
    case 'unconfirmed': return 'Playback test unconfirmed';
    case 'skipped': return 'Playback test skipped';
    case 'started': return 'Playback start unconfirmed';
    case 'expired': return 'Playback check expired';
    default: return 'Playback not tested';
  }
};

const playbackMetrics = (source: VerifiedSource): string => {
  const sample = source.playbackCheck;
  if (!sample || !playbackSampleStarted(source)) return '';
  const metrics: string[] = [];
  if (sample.secondsAdvanced && Number.isFinite(sample.secondsAdvanced) && sample.secondsAdvanced > 0) metrics.push(`${sample.secondsAdvanced.toFixed(1)}s of video progress`);
  if (sample.width && sample.height && Number.isFinite(sample.width) && Number.isFinite(sample.height) && sample.width > 0 && sample.height > 0) metrics.push(`${sample.width}×${sample.height}`);
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

const shelfPlaybackStarted = (movie: MovieDiscoveryItem, playback: FawesomeShelfPlaybackReport | undefined, now: number): boolean =>
  playback?.adBlockingEnabled === true && movie.playbackState === 'checked' && playbackSampleStarted(movie)
  && Number.isFinite(movie.playbackCheck?.durationSeconds) && (movie.playbackCheck?.durationSeconds ?? 0) >= 40 * 60
  && Number.isFinite(Date.parse(movie.playbackExpiresAt ?? '')) && Date.parse(movie.playbackExpiresAt ?? '') > now;

const shelfPlaybackLabel = (movie: MovieDiscoveryItem, playback: FawesomeShelfPlaybackReport | undefined, now: number): string => {
  if (shelfPlaybackStarted(movie, playback, now)) return 'Playback started · ad protection on';
  if (movie.playbackState === 'expired' || Date.parse(movie.playbackExpiresAt ?? '') <= now) return 'Playback check expired';
  const running = playback?.state === 'running' && Date.parse(playback.expiresAt) > now;
  if (running && movie.playbackState === 'checking') return 'Checking protected playback…';
  if (running && movie.playbackState === 'queued') return 'Queued for playback check';
  if (movie.playbackState === 'checked') {
    const reason = movie.playbackCheck?.reason;
    return reason ? PLAYBACK_REASON_LABELS[reason] : 'Playback unconfirmed';
  }
  return 'Playback unchecked';
};

const MovieDiscoveryShelf: React.FC<{ region: string; onSelect: (query: string) => void }> = ({ region, onSelect }) => {
  const [source, setSource] = useState<'charts' | 'fawesome'>('charts');
  const [mode, setMode] = useState<MovieDiscoveryMode>('popular');
  const [retry, setRetry] = useState(0);
  const [visibleLimit, setVisibleLimit] = useState(6);
  const [focusedMovieId, setFocusedMovieId] = useState<string | null>(null);
  const [now, setNow] = useState(Date.now);
  const requestKey = `${source}|${mode}|${region}`;
  const [state, setState] = useState<{ key: string; loading: boolean; report?: MovieDiscoveryReport; error?: string; playback?: FawesomeShelfPlaybackReport; playbackError?: string }>({ key: requestKey, loading: true });
  const current = state.key === requestKey ? state : null;
  const loading = current?.loading ?? true;
  const discovery = current?.report;
  const playback = current?.playback;
  const checkedAt = formatCheckedAt(discovery?.checkedAt);
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
        if (!response.ok) throw new Error('Movie suggestions could not load. You can still search any title above.');
        const report = response.data as MovieDiscoveryReport;
        if (!Array.isArray(report.items)) throw new Error('Movie suggestions could not load. You can still search any title above.');
        if (source === 'fawesome' && report.items.some((movie) => !isFawesomeMovieSuggestion(movie.sourceUrl))) throw new Error('Free Fawesome listings could not load. You can still search any title above.');
        if (active) setState({ key: requestKey, loading: false, report });
        if (!active || source !== 'fawesome' || report.items.length === 0) return;
        const catalogUrls = new Set(report.items.map((movie) => movie.sourceUrl));
        const readPlayback = (response: { ok: boolean; data: unknown }): FawesomeShelfPlaybackReport => {
          if (!response.ok) throw new Error('Background playback checks are unavailable. The catalog is still ready to search.');
          const snapshot = response.data as FawesomeShelfPlaybackReport;
          if (!/^[a-f0-9]{32}$/.test(snapshot.jobId ?? '') || snapshot.mode !== mode || snapshot.region !== 'ALL'
            || snapshot.adBlockingEnabled !== true || !['running', 'complete', 'cancelled', 'expired'].includes(snapshot.state)
            || !Number.isFinite(Date.parse(snapshot.expiresAt)) || !Array.isArray(snapshot.items)
            || snapshot.items.some((movie) => !isFawesomeMovieSuggestion(movie.sourceUrl) || !catalogUrls.has(movie.sourceUrl))) {
            throw new Error('Background playback checks could not be read. The catalog is still ready to search.');
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
            ? { ...previous, playback: previous.playback ? { ...previous.playback, state: 'cancelled' } : undefined, playbackError: 'Background playback checks stopped. The catalog is still ready to search.' } : previous);
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
                if (next.jobId !== snapshot.jobId) throw new Error('Background playback round changed.');
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
        if (active && !controller.signal.aborted) setState({ key: requestKey, loading: false, error: error instanceof Error ? error.message : 'Movie suggestions could not load. You can still search any title above.' });
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
        <h3 id="movie-discovery-heading">Explore movies</h3>
        <div className="preset-chips" role="group" aria-label="Choose movie suggestions">
          {(['popular', 'recent'] as const).map((option) => (
            <button key={option} type="button" className={`chip${source === 'charts' && mode === option ? ' scout-filter-active' : ''}`} aria-pressed={source === 'charts' && mode === option} onClick={() => { setSource('charts'); setMode(option); }}>
              {option === 'popular' ? 'Popular chart films' : 'Recent store releases'}
            </button>
          ))}
          <button type="button" className={`chip${source === 'fawesome' ? ' scout-filter-active' : ''}`} aria-pressed={source === 'fawesome'} onClick={() => { if (source !== 'fawesome') setMode('popular'); setSource('fawesome'); }}>Free on Fawesome</button>
        </div>
      </div>
      {source === 'fawesome' && <div className="preset-chips" role="group" aria-label="Choose Fawesome catalog" style={{ marginTop: 10 }}>
        {(['popular', 'recent'] as const).map((option) => <button key={option} type="button" className={`chip${mode === option ? ' scout-filter-active' : ''}`} aria-pressed={mode === option} onClick={() => setMode(option)}>{option === 'popular' ? 'Popular on Fawesome' : 'New on Fawesome'}</button>)}
      </div>}
      <p className="scout-muted">{source === 'fawesome'
        ? 'Free catalog listings. Protected playback checks run in the background; confirmed short samples appear first.'
        : mode === 'popular'
        ? 'Popular across sampled Apple storefronts. Dates show store releases; search a title to check watch options.'
        : 'Recent store releases from current charts; older reissues may appear.'}</p>
      {loading ? <p className="scout-muted" role="status">{source === 'fawesome' ? 'Loading free Fawesome listings…' : 'Loading movie charts…'}</p> : current?.error ? (
        <div className="movie-discovery-message" role="status"><p>{current.error}</p><button type="button" className="chip" onClick={() => setRetry((attempt) => attempt + 1)}>Retry suggestions</button></div>
      ) : discovery ? (
        <>
          {partial && <p className="scout-muted" role="status">Some provider feeds could not load. Available suggestions are shown below.</p>}
          {source === 'fawesome' && discovery.items.length > 0 && <div className="movie-discovery-progress" role="status" aria-live="polite">
            <p>{current?.playbackError ? current.playbackError
              : !playback ? 'Preparing protected playback checks…'
              : roundRunning ? `${playback.checkedCount} of ${playback.total} checked · ${startedCount} playback samples started · checking continues in the background.`
              : roundExpired ? `Background check round ended · ${startedCount} current playback samples started. Unchecked titles remain available to search.`
              : playback.state === 'cancelled' ? 'Background checks stopped. The catalog remains available to search.'
              : `Checks complete · ${startedCount} current playback samples started. Other titles remain unconfirmed or unchecked.`}</p>
            {(current?.playbackError || roundExpired) && <button type="button" className="chip" onClick={() => setRetry((attempt) => attempt + 1)}>Check again</button>}
          </div>}
          {discovery.items.length > 0 ? (
            <div className="movie-discovery-grid">
              {movies.slice(0, displayLimit).map((movie) => {
                const started = shelfPlaybackStarted(movie, playback, now);
                const sampleAt = formatCheckedAt(movie.playbackCheck?.checkedAt);
                return <button key={movie.id} type="button" className={`movie-discovery-title${started ? ' movie-discovery-started' : ''}`} onFocus={() => setFocusedMovieId(movie.id)} onBlur={() => setFocusedMovieId(null)} onClick={() => onSelect(movie.query || movie.title)}>
                  {movie.thumbnail && <img src={movie.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer" onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <span>
                    <strong>{movie.title}</strong>
                    <span className="scout-muted">{movie.sourceName}{movie.markets?.length ? ` · ${movie.markets.map((market) => countryName(market) ?? market).join(', ')}` : ''}</span>
                    {source === 'fawesome' ? movie.catalogDate && <span className="scout-muted">Catalog date: {movie.catalogDate}</span> : movie.releaseDate && <span className="scout-muted">Store release: {movie.releaseDate.slice(0, 10)}</span>}
                    {source === 'fawesome' && <>
                      <span className={`movie-discovery-playback${started ? ' sample-started' : ''}`}>{shelfPlaybackLabel(movie, playback, now)}</span>
                      {sampleAt && <span className="scout-muted">Test location: {countryName(movie.playbackCheck?.country) ?? 'unconfirmed'} · <time dateTime={movie.playbackCheck?.checkedAt}>{sampleAt}</time></span>}
                      {started && <span className="scout-muted">Short sample; full movie not verified.</span>}
                    </>}
                    <span className="movie-discovery-action">Find watch options <Search size={11} aria-hidden="true" /></span>
                  </span>
                </button>;
              })}
            </div>
          ) : <p className="scout-muted" role="status">{source === 'fawesome' ? 'No matching free catalog suggestions are available. Search a movie title above, or try the other catalog.' : 'No matching chart suggestions are available. Search a movie title above, or try another chart or region.'}</p>}
          {displayLimit < Math.min(movies.length, 24) && <button type="button" className="chip" style={{ marginTop: 10 }} onClick={() => setVisibleLimit((count) => Math.min(Math.max(count, displayLimit) + 6, 24))}>Show {Math.min(6, Math.min(movies.length, 24) - displayLimit)} more movies</button>}
          {discovery.detail && <details className="scout-provider-details" style={{ marginTop: 10 }}><summary>About these suggestions</summary><p>{discovery.detail}</p></details>}
          {checkedAt && <p className="scout-muted">{source === 'fawesome' ? 'Catalog checked' : 'Charts checked'} <time dateTime={discovery.checkedAt}>{checkedAt}</time>{discovery.cached ? ' · cached feed' : ''}</p>}
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

const audioSampleLabel = (source: VerifiedSource): string => {
  const check = source.playbackCheck;
  if (audioSampleStarted(source)) {
    const location = countryName(check?.country);
    return location ? `Playback started from ${location}` : 'Playback started';
  }
  if (!check || check.method === 'not_checked') return 'Playback not sampled';
  const reasonLabel = check.reason ? PLAYBACK_REASON_LABELS[check.reason] : undefined;
  if (reasonLabel) return reasonLabel;
  switch (check.status) {
    case 'not_started': return 'Playback did not start';
    case 'unsupported': return 'Playback test unsupported';
    case 'skipped': return 'Playback not sampled';
    default: return 'Playback sample unconfirmed';
  }
};

// The stream a track plays as: the chosen file first, the whole set behind
// it so the stage queues the rest in order.
const audioStream = (source: VerifiedSource, track?: AudioTrack): ActiveRelayStream => {
  const collection = source.creator ? `${source.title} · ${source.creator}` : source.title;
  const streamUrl = track?.url ?? (source.directStreamUrl as string);
  const current = track ?? source.tracks?.find((entry) => entry.url === streamUrl);
  const title = current && (source.tracks?.length ?? 0) > 1 ? `${current.title} · ${collection}` : collection;
  return {
    title, collection, streamUrl, pageUrl: source.url, provider: source.provider,
    format: current?.format ? `Direct ${current.format}` : source.streamFormat, useZeroTrustProxy: true, kind: 'audio', poster: source.thumbnail,
    durationSeconds: current?.durationSeconds, tracks: source.tracks,
  };
};

interface AudioResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionName: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The audio results: recordings with their performer, year, length and the
// reach of their file or page. The movie cards' playback and offer rows do
// not apply to a recording yet.
const AudioResults: React.FC<AudioResultsProps> = ({ report, sources, filter, onFilter, regionName, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{sources.length} {sources.length === 1 ? 'recording' : 'recordings'}</strong> for “{report.query}”<span className="scout-muted"> · {regionName}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">Audio search · Listings are matched by title and performer. Files are checked for reach from the server, and up to two free recordings get a short muted sample in the isolated browser; a started sample confirms the first file decodes, not the whole recording. The sample also takes an acoustic fingerprint of the file’s beginning, and recordings that sound the same are matched by it whatever their tags say.</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>Search coverage{coverageOpen ? ' · some sources unavailable' : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? 'Search completed.' : provider.status === 'empty' ? 'No matching listings.' : provider.status === 'not_configured' ? 'Not configured.' : 'Could not search this source.')}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>Automatic link checks</strong> · {report.availabilityChecks.checkedCount} checked{checkLocation ? ` from ${checkLocation}` : ' · check location unconfirmed'}</p>
          {report.availabilityChecks.playbackCheckedCount !== undefined && <p><strong>Muted playback samples</strong> · {report.availabilityChecks.playbackCheckedCount} attempted{report.availabilityChecks.playbackStartedCount !== undefined ? ` · ${report.availabilityChecks.playbackStartedCount} started` : ''}{report.availabilityChecks.fingerprintCount ? ` · ${report.availabilityChecks.fingerprintCount} acoustic ${report.availabilityChecks.fingerprintCount === 1 ? 'fingerprint' : 'fingerprints'} taken` : ''}. A started sample confirms the first file decodes, not the whole recording.</p>}
          {report.atlas?.fingerprints ? <p><strong>Fingerprint library</strong> · {report.atlas.fingerprints} stored in the index and compared with everything heard before.</p> : null}
          {batchCheckedAt && <p>Checked <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label="Filter by access type">
        {FILTERS.map(({ value, label }) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {label} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? 'No recordings found in the covered audio catalogs.' : `No listings marked ${filter} in these results.`}</strong>
          <p>{sources.length === 0 ? 'Try the recording’s title with its performer, as in “Kind of Blue by Miles Davis”. Search coverage is shown above.' : 'Choose another filter. Access labels reflect the evidence available for each source.'}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const checkedAt = formatCheckedAt(check?.checkedAt);
            const length = runtimeSeconds(source.duration);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const sampleStarted = audioSampleStarted(source);
            const sample = source.playbackCheck;
            const sampleCheckedAt = formatCheckedAt(sample?.checkedAt);
            const reach = sampleStarted ? 'Playback sample passed' : source.verificationStatus === 'media_reachable' ? 'Audio file reachable' : source.verificationStatus === 'page_reachable' ? 'Page reachable' : 'Listing';
            const tracks = source.tracks ?? [];
            // The index's verdict, compared with everything it has heard,
            // over the search's own between its two samples.
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const fingerprintNote = sample ? fingerprintLabel(sample) : null;
            return (
              <article key={source.id} className="source-card movie-source-card audio-source-card">
                <div>
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{ACCESS_LABELS[source.accessType ?? 'unknown']}</span>
                    <span className={`scout-muted${sampleStarted ? ' scout-media-status' : ''}`}>{reach}</span>
                  </div>
                  <div className="movie-source-title">
                    {source.thumbnail && <img src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                      onError={(event) => { event.currentTarget.hidden = true; }} />}
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">Performer unconfirmed</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}</p>
                      <p className="scout-muted movie-source-runtime">{length === null ? 'Length unconfirmed' : `Listed length: ${formatRuntime(length)}`}{source.verificationStatus === 'media_reachable' && source.streamFormat ? ` · ${source.streamFormat}` : ''}</p>
                    </div>
                  </div>
                  {source.accessType === 'free' && (
                    <div className={`movie-playback-check${sampleStarted ? ' sample-started' : ''}`}>
                      <p className="movie-check-status">{audioSampleLabel(source)}</p>
                      {sampleStarted
                        ? <p className="scout-muted">{(sample?.secondsAdvanced ?? 0).toFixed(1)}s of audio decoded in the isolated browser; the rest of the recording is not verified.</p>
                        : <p className="scout-muted">{sample?.detail || 'No playback sample was tested for this recording.'}</p>}
                      {fingerprintNote && <p className="scout-muted">{fingerprintNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      {sampleCheckedAt && <p className="scout-muted">Sample checked <time dateTime={sample?.checkedAt}>{sampleCheckedAt}</time></p>}
                    </div>
                  )}
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  {tracks.length > 1 && (
                    <details className="scout-provider-details audio-track-list">
                      <summary>{tracks.length} tracks · play from any</summary>
                      <ol>{tracks.map((track, index) => {
                        const trackLength = track.durationSeconds && track.durationSeconds > 0 ? formatRuntime(track.durationSeconds) : null;
                        return (
                          <li key={track.url}>
                            <button type="button" className="btn" disabled={!canPlayAudio(source)} onClick={() => onPlayRelay(audioStream(source, track))}>
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
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{accessCheckLabel(check)}</p>
                      {check?.detail && <p className="scout-muted">{check.detail}</p>}
                      <p className="scout-muted">A reachable file does not confirm playback; only a started sample does, and only for its beginning.</p>
                      {checkedAt && <p className="scout-muted">Link checked <time dateTime={check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  <button type="button" className="btn btn-primary" disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? 'Opening protected browser…' : 'Open page with ad blocking'}
                  </button>
                  {canPlayAudio(source) && <button type="button" className="btn" onClick={() => onPlayRelay(audioStream(source))}>
                    <Play size={13} aria-hidden="true" /> {sampleStarted ? (tracks.length > 1 ? 'Play sampled recording · all tracks' : 'Play sampled recording') : (tracks.length > 1 ? 'Play all tracks through the relay' : 'Play through the relay')}
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

// A picture opens in the zoomable viewer through the relay whenever its
// file answered, decoded or not: the viewer can show what the probe cannot
// decode yet, such as WebP.
const canViewImage = (source: VerifiedSource): boolean =>
  Boolean(source.directStreamUrl) && (source.imageCheck?.status === 'decoded' || source.imageCheck?.status === 'unsupported');

const imageStream = (source: VerifiedSource): ActiveRelayStream => ({
  title: source.creator ? `${source.title} · ${source.creator}` : source.title,
  streamUrl: source.directStreamUrl as string, pageUrl: source.url, provider: source.provider,
  format: source.streamFormat || (source.imageCheck?.format ? `${source.imageCheck.format.toUpperCase()} image` : 'Image'),
  resolution: source.imageCheck?.width && source.imageCheck?.height ? `${source.imageCheck.width}×${source.imageCheck.height}` : source.resolution,
  useZeroTrustProxy: true, kind: 'image', poster: source.thumbnail,
});

const imageProbeLabel = (check?: ImageCheck): string => {
  switch (check?.status) {
    case 'decoded': return `Decoded ${check.width}×${check.height} ${(check.format ?? '').toUpperCase()}`.trim();
    case 'unsupported': return `File reachable · ${(check.format ?? 'this').toUpperCase()} not decoded yet`;
    case 'undecodable': return 'File answered but is not a picture';
    case 'unreachable': return check.httpStatus ? `File answered HTTP ${check.httpStatus}` : 'File unreachable';
    default: return 'File not probed';
  }
};

const imageHashLabel = (check?: ImageCheck): string | null => {
  switch (check?.fingerprintStatus) {
    case 'computed': return 'Perceptual hash taken from the decoded picture; the index compares it with every picture it has seen.';
    case 'too_large': return 'No perceptual hash: the file is larger than the probe decodes whole and the host gave no thumbnail.';
    case 'undecodable': return 'No perceptual hash: the thumbnail could not be decoded.';
    case 'unavailable': return 'No perceptual hash: the thumbnail could not be fetched.';
    default: return null;
  }
};

interface ImageResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionName: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The picture results: each picture with its maker, year, dimensions,
// licence, what the probe decoded of its file, and what its hash said
// beside the other pictures of the search and the index.
const ImageResults: React.FC<ImageResultsProps> = ({ report, sources, filter, onFilter, regionName, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{sources.length} {sources.length === 1 ? 'picture' : 'pictures'}</strong> for “{report.query}”<span className="scout-muted"> · {regionName}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">Picture search · The host matched these by its own index, so a file name need not say what the picture shows. Each file is fetched for its header, which gives the dimensions and format, and its thumbnail is decoded for a perceptual hash; pictures that look the same are matched by it whatever their names say. No sandbox render runs yet.</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>Search coverage{coverageOpen ? ' · some sources unavailable' : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? 'Search completed.' : provider.status === 'empty' ? 'No matching listings.' : provider.status === 'not_configured' ? 'Not configured.' : 'Could not search this source.')}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>File probes</strong> · {report.availabilityChecks.checkedCount} checked{checkLocation ? ` from ${checkLocation}` : ' · check location unconfirmed'}{report.availabilityChecks.fingerprintCount ? ` · ${report.availabilityChecks.fingerprintCount} perceptual ${report.availabilityChecks.fingerprintCount === 1 ? 'hash' : 'hashes'} taken` : ''}</p>
          {report.atlas?.fingerprints ? <p><strong>Fingerprint library</strong> · {report.atlas.fingerprints} stored in the index and compared with every picture seen before.</p> : null}
          {batchCheckedAt && <p>Checked <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label="Filter by access type">
        {FILTERS.map(({ value, label }) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {label} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? 'No pictures found in the covered catalogs.' : `No listings marked ${filter} in these results.`}</strong>
          <p>{sources.length === 0 ? 'Try what the picture shows in other words, or the artist, as in “Starry Night by Van Gogh”. Search coverage is shown above.' : 'Choose another filter. Access labels reflect the evidence available for each source.'}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const probe = source.imageCheck;
            const checkedAt = formatCheckedAt(probe?.checkedAt ?? check?.checkedAt);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const hashNote = imageHashLabel(probe);
            const dimensions = probe?.width && probe?.height ? `${probe.width}×${probe.height}` : source.resolution ? source.resolution.replace('x', '×') : null;
            return (
              <article key={source.id} className="source-card movie-source-card image-source-card">
                <div>
                  {source.thumbnail && <img className="image-thumb" src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                    onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{ACCESS_LABELS[source.accessType ?? 'unknown']}</span>
                    <span className={`scout-muted${probe?.status === 'decoded' ? ' scout-media-status' : ''}`}>{probe ? imageProbeLabel(probe) : source.verificationStatus === 'page_reachable' ? 'Page reachable' : 'Listing'}</span>
                  </div>
                  <div className="movie-source-title">
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">Maker unconfirmed</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}{dimensions ? ` · ${dimensions}` : ''}{source.license ? ` · ${source.license}` : ''}</p>
                    </div>
                  </div>
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  <div className="movie-source-facts">
                    {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                    <div className="movie-access-check">
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{probe ? imageProbeLabel(probe) : accessCheckLabel(check)}</p>
                      {(probe?.detail || check?.detail) && <p className="scout-muted">{probe?.detail || check?.detail}</p>}
                      {hashNote && <p className="scout-muted">{hashNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      <p className="scout-muted">A decoded header confirms the file is a picture of these dimensions; no sandbox has rendered it yet.</p>
                      {checkedAt && <p className="scout-muted">File checked <time dateTime={probe?.checkedAt ?? check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  {canViewImage(source) && <button type="button" className="btn btn-primary" onClick={() => onPlayRelay(imageStream(source))}>
                    <Scan size={13} aria-hidden="true" /> Open in the viewer
                  </button>}
                  <button type="button" className={`btn${canViewImage(source) ? '' : ' btn-primary'}`} disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? 'Opening protected browser…' : 'Open page with ad blocking'}
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
// the relay's answer again before framing it. Plain text and the formats
// the probe cannot read yet open in the protected browser.
const canReadDocument = (source: VerifiedSource): boolean =>
  Boolean(source.directStreamUrl) && isPdfUrl(source.directStreamUrl as string)
  && (source.documentCheck?.status === 'parsed' || source.documentCheck?.status === 'not_checked');

const documentStream = (source: VerifiedSource): ActiveRelayStream => ({
  title: source.creator ? `${source.title} · ${source.creator}` : source.title,
  streamUrl: source.directStreamUrl as string, pageUrl: source.url, provider: source.provider,
  format: source.streamFormat || (source.documentCheck?.format ? `${source.documentCheck.format.toUpperCase()} document` : 'Document'),
  useZeroTrustProxy: true, kind: 'document', poster: source.thumbnail,
});

const documentProbeLabel = (check?: DocumentCheck): string => {
  switch (check?.status) {
    case 'parsed': {
      const format = (check.format ?? '').toUpperCase();
      if (check.pages) return `Parsed ${format} · ${check.pages} ${check.pages === 1 ? 'page' : 'pages'}${check.pagesConfirmed ? '' : ' by the host’s count'}`;
      return `Parsed ${format}`.trim();
    }
    case 'unsupported': return `File reachable · ${(check.format ?? 'this').toUpperCase()} not read yet`;
    case 'undecodable': return 'File answered but is not a document';
    case 'unreachable': return check.httpStatus ? `File answered HTTP ${check.httpStatus}` : 'File unreachable';
    default: return 'File not probed';
  }
};

const documentFingerprintLabel = (check?: DocumentCheck): string | null => {
  switch (check?.fingerprintStatus) {
    case 'computed': {
      const from = check.textSource === 'ocr_text' ? 'the OCR text beside the file' : check.textSource === 'text_file' ? 'the text file' : 'the PDF’s own text';
      return `Text fingerprint taken from ${from}; the index compares it with every document it has read.`;
    }
    case 'too_short': return 'No text fingerprint: too little text was read, as with a scan that carries no text layer and no OCR text beside it.';
    default: return null;
  }
};

interface DocumentResultsProps {
  report: SearchReport;
  sources: VerifiedSource[];
  filter: AccessFilter;
  onFilter: (filter: AccessFilter) => void;
  regionName: string;
  checkLocation: string | null;
  batchCheckedAt: string | null;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
  onPlayRelay: (stream: ActiveRelayStream) => void;
}

// The document results: each document with its author, year, page count
// and collection, what the probe read of its file, and what its text
// fingerprint said beside the other documents of the search and the index.
const DocumentResults: React.FC<DocumentResultsProps> = ({ report, sources, filter, onFilter, regionName, checkLocation, batchCheckedAt, launchingUrl, onLaunchRbi, onPlayRelay }) => {
  const visible = sources.filter((source) => filter === 'all' || accessGroup(source) === filter);
  const coverageOpen = report.providers?.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ?? false;
  return (
    <>
      <div className="movie-results-summary" role="status">
        <div><strong>{sources.length} {sources.length === 1 ? 'document' : 'documents'}</strong> for “{report.query}”<span className="scout-muted"> · {regionName}</span></div>
        <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
      </div>
      <p className="scout-muted">Document search · The host matched these by its own catalog. Each PDF is fetched at both ends for its format and its own page count, and its text, from the OCR text beside it or from the PDF itself, is fingerprinted; documents that read the same are matched by it whatever their titles say. No sandbox render runs yet.</p>
      {report.providers && report.providers.length > 0 && (
        <details className="scout-provider-details" open={coverageOpen}>
          <summary>Search coverage{coverageOpen ? ' · some sources unavailable' : ''}</summary>
          <ul>{report.providers.map((provider) => (
            <li key={provider.name}>
              <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? 'Search completed.' : provider.status === 'empty' ? 'No matching listings.' : provider.status === 'not_configured' ? 'Not configured.' : 'Could not search this source.')}
            </li>
          ))}</ul>
        </details>
      )}
      {report.availabilityChecks && (
        <div className="scout-link-check-summary">
          <p><strong>File probes</strong> · {report.availabilityChecks.checkedCount} checked{checkLocation ? ` from ${checkLocation}` : ' · check location unconfirmed'}{report.availabilityChecks.fingerprintCount ? ` · ${report.availabilityChecks.fingerprintCount} text ${report.availabilityChecks.fingerprintCount === 1 ? 'fingerprint' : 'fingerprints'} taken` : ''}</p>
          {report.atlas?.fingerprints ? <p><strong>Fingerprint library</strong> · {report.atlas.fingerprints} stored in the index and compared with every document read before.</p> : null}
          {batchCheckedAt && <p>Checked <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
        </div>
      )}
      <div className="preset-chips" role="group" aria-label="Filter by access type">
        {FILTERS.map(({ value, label }) => (
          <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
            aria-pressed={filter === value} onClick={() => onFilter(value)}>
            {label} ({value === 'all' ? sources.length : sources.filter((source) => accessGroup(source) === value).length})
          </button>
        ))}
      </div>
      {visible.length === 0 ? (
        <div className="scout-empty" role="status">
          <strong>{sources.length === 0 ? 'No documents found in the covered collections.' : `No listings marked ${filter} in these results.`}</strong>
          <p>{sources.length === 0 ? 'Try the title in other words, or add the author, as in “Alice in Wonderland by Lewis Carroll”. Search coverage is shown above.' : 'Choose another filter. Access labels reflect the evidence available for each source.'}</p>
        </div>
      ) : (
        <div className="sources-grid movie-sources-grid">
          {visible.map((source) => {
            const check = source.availabilityCheck;
            const probe = source.documentCheck;
            const checkedAt = formatCheckedAt(probe?.checkedAt ?? check?.checkedAt);
            const showYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
            const identity = report.atlas?.identities?.[source.url] ?? source.identity;
            const fingerprintNote = documentFingerprintLabel(probe);
            const pages = probe?.pages || source.pages;
            return (
              <article key={source.id} className="source-card movie-source-card image-source-card document-source-card">
                <div>
                  {source.thumbnail && <img className="image-thumb" src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                    onError={(event) => { event.currentTarget.hidden = true; }} />}
                  <div className="movie-source-badges">
                    <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{ACCESS_LABELS[source.accessType ?? 'unknown']}</span>
                    <span className={`scout-muted${probe?.status === 'parsed' ? ' scout-media-status' : ''}`}>{probe ? documentProbeLabel(probe) : source.verificationStatus === 'page_reachable' ? 'Page reachable' : 'Listing'}</span>
                  </div>
                  <div className="movie-source-title">
                    <div>
                      <h3>{source.title}{showYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                      <p className="movie-source-provider">{source.creator || <span className="scout-muted">Author unconfirmed</span>}</p>
                      <p className="scout-muted movie-source-host">{source.provider} · {sourceHost(source.url)}{pages ? ` · ${pages} ${pages === 1 ? 'page' : 'pages'}` : ''}{source.license ? ` · ${source.license}` : ''}</p>
                    </div>
                  </div>
                  {source.description && <p className="scout-description movie-source-description">{source.description}</p>}
                  <div className="movie-source-facts">
                    {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                    <div className="movie-access-check">
                      <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{probe ? documentProbeLabel(probe) : accessCheckLabel(check)}</p>
                      {(probe?.detail || check?.detail) && <p className="scout-muted">{probe?.detail || check?.detail}</p>}
                      {fingerprintNote && <p className="scout-muted">{fingerprintNote}</p>}
                      {identity && (
                        <p className={`audio-identity identity-${identity.effect ?? identity.verdict ?? 'none'}`}>
                          <span className="ladder-identity"><Fingerprint size={11} aria-hidden="true" /> {identityLabel(identity)}</span>
                          {identity.detail && <span className="scout-muted">{identity.detail}</span>}
                        </p>
                      )}
                      <p className="scout-muted">A parsed file confirms the document is a PDF of these pages; no sandbox has rendered it yet.</p>
                      {checkedAt && <p className="scout-muted">File checked <time dateTime={probe?.checkedAt ?? check?.checkedAt}>{checkedAt}</time></p>}
                    </div>
                  </div>
                </div>
                <div className="movie-source-actions">
                  {canReadDocument(source) && <button type="button" className="btn btn-primary" onClick={() => onPlayRelay(documentStream(source))}>
                    <BookOpen size={13} aria-hidden="true" /> Open in the reader
                  </button>}
                  <button type="button" className={`btn${canReadDocument(source) ? '' : ' btn-primary'}`} disabled={launchingUrl !== null}
                    onClick={() => onLaunchRbi(source.url, false)}>
                    <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? 'Opening protected browser…' : 'Open page with ad blocking'}
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
  const regionName = REGIONS.find(([code]) => code === reportRegion)?.[1] ?? reportRegion;
  const selectedRegionName = REGIONS.find(([code]) => code === region)?.[1] ?? region;
  const allAccessUnconfirmed = sources.length > 0 && sources.every((source) => accessGroup(source) === 'unknown');
  const checkLocation = countryName(report?.availabilityChecks?.country);
  const batchCheckedAt = formatCheckedAt(report?.availabilityChecks?.checkedAt);

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
          <h2 id="movie-search-heading">{kind === 'audio' ? 'Find recordings across open audio catalogs.' : kind === 'image' ? 'Find pictures across open media catalogs.' : kind === 'document' ? 'Find documents across open text collections.' : 'Find full-length movies across the web.'}</h2>
          <p className="scout-description">{kind === 'audio'
            ? 'Search the Internet Archive’s audio collections by title, with the performer if known. Each listing shows its performer, year and length; files are checked for reach, and up to two get a short muted sample.'
            : kind === 'image'
              ? 'Search Wikimedia Commons by what the picture shows, with the artist if known. Each file is fetched for its dimensions and hashed, so the same picture is recognised under any name.'
              : kind === 'document'
                ? 'Search the Internet Archive’s texts by title, with the author if known. Each PDF is fetched at both ends for its format and page count, and its text is fingerprinted, so the same text is recognised under any title.'
                : region === 'ALL'
              ? 'Search recent releases, popular titles and independent cinema globally. Find free and paid options; playback samples show which supported free sources actually start.'
              : `Search globally with a preference for ${selectedRegionName}. Playback badges show the actual test location; prices and full playback still need confirmation.`}</p>
        </div>
        <button type="button" className="btn btn-theater scout-benchmark" disabled={searching}
          onClick={onRunBenchmark} title="Check the ad-blocking engine against the local security benchmark">
          <Sparkles size={13} aria-hidden="true" /> Test ad blocking
        </button>
      </div>

      <div className="preset-chips movie-kind-toggle" role="group" aria-label="What to find">
        <button type="button" className={`chip${kind === 'video' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'video'} onClick={() => setKind('video')}>
          <Film size={12} aria-hidden="true" /> Movies
        </button>
        <button type="button" className={`chip${kind === 'audio' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'audio'} onClick={() => setKind('audio')}>
          <Music size={12} aria-hidden="true" /> Audio
        </button>
        <button type="button" className={`chip${kind === 'image' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'image'} onClick={() => setKind('image')}>
          <ImageIcon size={12} aria-hidden="true" /> Pictures
        </button>
        <button type="button" className={`chip${kind === 'document' ? ' scout-filter-active' : ''}`} aria-pressed={kind === 'document'} onClick={() => setKind('document')}>
          <FileText size={12} aria-hidden="true" /> Documents
        </button>
      </div>

      <form className="search-row movie-search-form" onSubmit={(event) => { event.preventDefault(); search(query); }}>
        <label className="movie-query-field">
          <span className="scout-field-label">{kind === 'audio' ? 'Recording title, with the performer if known' : kind === 'image' ? 'What the picture shows, with the artist if known' : kind === 'document' ? 'Document title, with the author if known' : 'Movie title or keywords'}</span>
          <input className="search-input" value={query} onChange={(event) => setQuery(event.target.value)}
            placeholder={kind === 'audio' ? 'Title by performer, e.g. Kind of Blue by Miles Davis' : kind === 'image' ? 'Subject by artist, e.g. Starry Night by Van Gogh' : kind === 'document' ? 'Title by author, e.g. Alice in Wonderland by Lewis Carroll' : 'Movie title and year, or a movie page URL'} maxLength={500} required />
        </label>
        <label className="movie-region-field">
          <span className="scout-field-label">Region preference (optional)</span>
          <select className="search-input" value={region} onChange={(event) => setRegion(event.target.value)}>
            {REGIONS.map(([code, label]) => <option key={code} value={code}>{label}</option>)}
          </select>
        </label>
        <button type="submit" className="btn btn-primary movie-search-submit" disabled={!query.trim()}>
          <Search size={14} aria-hidden="true" /> {searching ? 'Search again' : kind === 'audio' ? 'Find recordings' : kind === 'image' ? 'Find pictures' : kind === 'document' ? 'Find documents' : 'Find movie pages'}
        </button>
        {kind === 'video' && (
          <label className="movie-length-preference">
            <input type="checkbox" checked={fullLengthOnly} onChange={(event) => setFullLengthOnly(event.target.checked)}
              aria-describedby="movie-length-help" />
            <span><strong>Full-length movies</strong><span id="movie-length-help" className="scout-muted">Show listings of 40 minutes or longer. Missing runtimes stay marked length unconfirmed. Turn off to include shorts.</span></span>
          </label>
        )}
      </form>

      {kind === 'video' ? (
        <>
          <MovieDiscoveryShelf region={region} onSelect={(title) => { setQuery(title); search(title); }} />
          <div>
            <div className="preset-chips" role="group" aria-label="Try supported free catalogs">
              <span className="scout-muted">Try supported free catalogs:</span>
              {FREE_CATALOG_EXAMPLES.map((title) => <button key={title} type="button" className="chip" onClick={() => { setQuery(title); search(title); }}>{title}</button>)}
            </div>
            <p className="scout-muted">Film examples; search to see access checks and any confirmed playback sample.</p>
          </div>
        </>
      ) : kind === 'audio' ? (
        <p className="scout-muted">Audio results come from the Internet Archive’s audio collections, read through the same driver runtime as the movie catalogs. A recording is matched by its title, performer and length, its files are checked for reach and sampled, and a multi-track item plays in order through the relay. The sample takes an acoustic fingerprint of the file’s beginning; the index compares it with every fingerprint it holds, so two listings of the same recording are matched by what they sound like, and a different recording under the same tags is kept apart.</p>
      ) : kind === 'image' ? (
        <p className="scout-muted">Picture results come from Wikimedia Commons, read through the same driver runtime as the other catalogs. Each file is fetched for its header, which gives the dimensions and format, and its thumbnail is decoded for a perceptual hash; the index compares the hash with every picture it has seen, so the same picture is matched under any file name and a different picture under the same name is kept apart. A picture opens in the zoomable viewer through the relay.</p>
      ) : (
        <p className="scout-muted">Document results come from the Internet Archive’s texts, read through the same driver runtime as the other catalogs. Each item’s metadata names its PDF and the OCR text beside it; the PDF is fetched at both ends for its format and its own page count, and the text is fingerprinted, so the index matches the same text under any title and keeps a different text under the same title apart. A PDF opens in the reader through the relay; other formats open in the protected browser.</p>
      )}
      <p className="scout-protection-note"><Shield size={14} aria-hidden="true" /> Sites open in ITEMBA's browser with ad and tracker blocking enabled. Some ads may remain; subscriptions and regional restrictions still apply.</p>
      {kind === 'video' && <p className="scout-muted">Native samples cover supported free catalogs, including NASA+, NFB, creator-authorized Catsuka and matching Fawesome movie pages. Provider checks can report restrictions for Pluto TV and Filmzie. Playback badges require decoded movie content; pages, ads and trailers do not qualify.</p>}

      {searchError && <p className="scout-error" role="alert">{searchError}</p>}
      {launchError && <p className="scout-error" role="alert">{launchError}</p>}
      {searching && <p className="scout-empty" role="status">{kind === 'audio' ? 'Finding recordings in open audio catalogs and checking their files…' : kind === 'image' ? 'Finding pictures in open media catalogs and decoding their files…' : kind === 'document' ? 'Finding documents in open text collections and reading their files…' : 'Finding global movie sources and testing free playback…'}</p>}

      <RankedLocations known={known} merged={merged} report={report} searching={searching} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} />

      {report && !searching && !searchError && report.kind === 'audio' && (
        <AudioResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionName={regionName}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind === 'image' && (
        <ImageResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionName={regionName}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind === 'document' && (
        <DocumentResults report={report} sources={sources} filter={filter} onFilter={setFilter} regionName={regionName}
          checkLocation={checkLocation} batchCheckedAt={batchCheckedAt} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} onPlayRelay={onPlayRelay} />
      )}

      {report && !searching && !searchError && report.kind !== 'audio' && report.kind !== 'image' && report.kind !== 'document' && (
        <>
          <div className="movie-results-summary" role="status">
            <div><strong>{sources.length} {sources.length === 1 ? 'movie page' : 'movie pages'}</strong> for “{report.query}”<span className="scout-muted"> · {regionName}</span></div>
            <span className="scout-muted scout-duration"><Clock size={12} aria-hidden="true" /> {(report.durationMs / 1000).toFixed(1)}s</span>
          </div>
          {report.fullLengthOnly === true && <p className="scout-muted">Full-length search · Known runtimes of 40+ minutes; unknown lengths remain unconfirmed.</p>}
          {report.region === 'ALL' && <p className="scout-muted">Worldwide discovery searches configured web sources, open catalogs and selected storefronts. Results are not limited to the playback check location. Each listing shows its market when known.</p>}
          {report.providers && report.providers.length > 0 && (
            <details className="scout-provider-details" open={report.providers.some((provider) => provider.status === 'error' || provider.status === 'not_configured')}>
              <summary>Search coverage{report.providers.some((provider) => provider.status === 'error' || provider.status === 'not_configured') ? ' · some sources unavailable' : ''}</summary>
              <ul>{report.providers.map((provider) => (
                <li key={provider.name}>
                  <strong>{provider.name}</strong>: {provider.detail || (provider.status === 'ok' ? 'Search completed.' : provider.status === 'empty' ? 'No matching listings.' : provider.status === 'not_configured' ? 'Not configured.' : 'Could not search this source.')}
                </li>
              ))}</ul>
            </details>
          )}
          {report.availabilityChecks && (
            <div className="scout-link-check-summary">
              <p><strong>Automatic link checks</strong> · {report.availabilityChecks.checkedCount} checked{checkLocation ? ` from ${checkLocation}` : ' · check location unconfirmed'}</p>
              {report.availabilityChecks.playbackCheckedCount !== undefined && <p><strong>Free playback samples</strong> · {report.availabilityChecks.playbackCheckedCount} attempted{report.availabilityChecks.playbackStartedCount !== undefined ? ` · ${report.availabilityChecks.playbackStartedCount} started` : ''}. Short samples do not verify the full movie.</p>}
              {(report.availabilityChecks.providerCheckedCount ?? 0) > 0 && <p><strong>Provider access checks</strong> · {report.availabilityChecks.providerCheckedCount}. These detect restrictions; movie playback requires a separate decoded sample.</p>}
              {reportRegion === 'ALL' && <p>{checkLocation ? `Playback checks run from ${checkLocation}.` : 'Playback check location is unconfirmed.'} Availability elsewhere remains untested.</p>}
              {report.availabilityChecks.detail && <p>{report.availabilityChecks.detail}</p>}
              {batchCheckedAt && <p>Checked <time dateTime={report.availabilityChecks.checkedAt}>{batchCheckedAt}</time></p>}
            </div>
          )}
          <div className="preset-chips" role="group" aria-label="Filter by access type">
            {FILTERS.map(({ value, label }) => (
              <button key={value} type="button" className={`chip${filter === value ? ' scout-filter-active' : ''}`}
                aria-pressed={filter === value} onClick={() => setFilter(value)}>
                {label} ({value === 'all' ? sampledSources.length : sampledSources.filter((source) => accessGroup(source) === value).length})
              </button>
            ))}
          </div>
          <label className="movie-length-preference">
            <input type="checkbox" checked={playbackOnly} onChange={(event) => setPlaybackOnly(event.target.checked)} aria-describedby="movie-playback-filter-help" />
            <span><strong>Playback started only</strong><span id="movie-playback-filter-help" className="scout-muted">Show passed short samples. Complete movie playback remains unverified.</span></span>
          </label>
          {allAccessUnconfirmed && <p className="scout-muted">No free or paid movie offers are confirmed in these results. Access remains unconfirmed until checked on the movie page.</p>}

          {visibleSources.length === 0 ? (
            <div className="scout-empty" role="status">
              <strong>{sources.length === 0 ? `No direct movie pages found ${reportRegion === 'ALL' ? 'across the covered markets' : `for ${regionName}`}.` : playbackOnly ? 'No started playback samples match these filters.' : filter === 'unknown' ? 'No pages with unconfirmed access in these results.' : `No listings marked ${filter} in these results.`}</strong>
              <p>{sources.length === 0 ? `Try the exact movie title and release year${report.fullLengthOnly ? ', or turn off Full-length movies to include shorts' : ', or choose another region'}. Search coverage is shown above.` : playbackOnly ? 'Choose All or Free, or clear Playback started only to view unverified options. Automatic samples cover supported free media.' : 'Choose another filter or try another region. Access labels reflect the evidence available for each source.'}</p>
            </div>
          ) : (
            <div className="sources-grid movie-sources-grid">
              {visibleSources.map((source) => {
                const check = source.availabilityCheck;
                const checkedAt = formatCheckedAt(check?.checkedAt);
                const checkCountry = countryName(check?.country);
                const listingMarket = marketCode(source.region);
                const playback = source.playbackCheck;
                const providerPreflight = playback?.method === 'provider_playback_preflight';
                const sampleStarted = playbackSampleStarted(source);
                const sampleCountry = countryName(playback?.country);
                const sampleMatchesRegion = Boolean(sampleStarted && (reportRegion === 'ALL' || (sampleCountry && marketCode(playback?.country) === reportRegion)));
                const regionHeadline = sampleStarted
                  ? reportRegion === 'ALL' ? 'Playback sample passed' : sampleCountry
                    ? sampleMatchesRegion ? `Playback started in ${sampleCountry}` : `Sample in ${sampleCountry} · ${regionName} unconfirmed`
                    : 'Sample started · check location unconfirmed'
                  : availabilityLabel(source, reportRegion, regionName);
                const sampleCheckedAt = formatCheckedAt(playback?.checkedAt);
                const sampleMetrics = playbackMetrics(source);
                const sampleDuration = playback?.method === 'isolated_video_sample' ? playback.durationSeconds : undefined;
                const measuredRuntime = typeof sampleDuration === 'number' && Number.isFinite(sampleDuration) && sampleDuration > 0 ? sampleDuration : null;
                const listedRuntime = runtimeSeconds(source.duration);
                const movieRuntime = measuredRuntime ?? listedRuntime;
                const showReleaseYear = source.releaseYear && !source.title.includes(String(source.releaseYear));
                const description = source.id.startsWith('ref-') && source.verificationStatus !== 'media_reachable'
                  ? 'Reference catalog entry. Current media availability has not been confirmed; open the source to check.'
                  : source.description;
                const canRelay = canTryFreeMedia(source);
                const evidenceLinks = [
                  { label: 'Open access evidence', url: source.accessEvidenceUrl },
                  { label: 'Open regional evidence', url: source.availabilityEvidenceUrl },
                  { label: 'Open checked page', url: check?.evidenceUrl },
                  { label: 'Open playback evidence', url: playback?.evidenceUrl },
                ].filter((evidence): evidence is { label: string; url: string } => Boolean(evidence.url && evidence.url !== source.url))
                  .filter((evidence, index, entries) => entries.findIndex((entry) => entry.url === evidence.url) === index);
                return (
                  <article key={source.id} className="source-card movie-source-card">
                    <div>
                      <div className="movie-source-badges">
                        <span className={`pill-badge${source.accessType === 'free' ? ' live' : ''}`}>{ACCESS_LABELS[source.accessType ?? 'unknown']}</span>
                        <span className={`scout-muted${sampleMatchesRegion ? ' scout-media-status' : ''}`}>{regionHeadline}</span>
                      </div>
                      <div className="movie-source-title">
                        {source.thumbnail && <img src={source.thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer"
                          onError={(event) => { event.currentTarget.hidden = true; }} />}
                        <div>
                          <h3>{source.title}{showReleaseYear ? <span className="scout-muted"> ({source.releaseYear})</span> : null}</h3>
                          <p className="movie-source-provider">{source.provider}</p>
                          <p className="scout-muted movie-source-host">{sourceHost(source.url)}</p>
                          <p className="scout-muted movie-source-runtime">{movieRuntime === null ? 'Length unconfirmed' : `${measuredRuntime !== null ? 'Media runtime' : 'Listed runtime'}: ${formatRuntime(movieRuntime)} · ${movieRuntime >= 40 * 60 ? measuredRuntime !== null ? 'Feature-length media' : 'Feature-length listing' : 'Short film / clip'}`}</p>
                          {source.provider === 'Fawesome' && !source.releaseYear && <p className="scout-muted">Original release year unconfirmed</p>}
                        </div>
                      </div>
                      <div className={`movie-playback-check${sampleStarted ? ' sample-started' : ''}`}>
                        <p className="movie-check-status">{playbackCheckLabel(source, reportRegion)}</p>
                        {sampleStarted ? <p className="scout-muted">Short sample; full movie not verified.</p> : (
                          <p className="scout-muted">{source.accessType !== 'free'
                            ? 'Automatic playback tests cover free media only. Sign-in and payment are not tested.'
                            : playback?.detail || 'No playback sample was tested for this result.'}</p>
                        )}
                        {providerPreflight && <p className="scout-muted">Provider access check; main movie playback was not verified.</p>}
                        {sampleMetrics && <p className="scout-muted">{sampleMetrics}</p>}
                        {playback && (reportRegion === 'ALL' || !sampleStarted) && source.accessType === 'free' && <p className="scout-muted">Test location: {sampleCountry ?? 'unconfirmed'}</p>}
                        {sampleCheckedAt && <p className="scout-muted">{providerPreflight ? 'Provider checked' : 'Playback checked'} <time dateTime={playback?.checkedAt}>{sampleCheckedAt}</time></p>}
                      </div>
                      {description && <p className="scout-description movie-source-description">{description}</p>}
                      <div className="movie-source-facts">
                        <p>{accessSummary(source)}</p>
                        {source.accessNote && <p className="scout-muted">{source.accessNote}</p>}
                        {listingMarket && <p className="scout-muted">Listing market: {listingMarket}</p>}
                        {source.availabilityNote && <p className="scout-muted">{source.availabilityNote}</p>}
                        <div className="movie-access-check">
                          <p className={`movie-check-status check-${check?.status ?? 'unchecked'}`}>{accessCheckLabel(check)}</p>
                          {check?.detail && <p className="scout-muted">{check.detail}</p>}
                          {check && !['page_reachable', 'media_reachable'].includes(check.status) && <p className="scout-muted">Check location: {checkCountry ?? 'unconfirmed'}</p>}
                          {!check && source.verificationStatus === 'media_reachable' && <p className="scout-muted">Media URL reachable during discovery; regional access unchecked.</p>}
                          <p className="scout-muted">A reachable link does not confirm playback.</p>
                          {checkedAt && <p className="scout-muted">Link checked <time dateTime={check?.checkedAt}>{checkedAt}</time></p>}
                        </div>
                      </div>
                      {evidenceLinks.length > 0 && (
                        <details className="scout-provider-details movie-source-evidence">
                          <summary>Access and regional evidence</summary>
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
                        <Globe size={14} aria-hidden="true" /> {launchingUrl === source.url ? 'Opening protected browser…' : 'Open movie page with ad blocking'}
                      </button>
                      {canRelay && <button type="button" className="btn" onClick={() => handleSelectRelay(source)}>
                        <Play size={13} aria-hidden="true" /> {sampleStarted ? 'Play sampled free media' : 'Try free media playback'}
                      </button>}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
          {(report.prunedSources?.length ?? 0) > 0 && (
            <details className="scout-provider-details">
              <summary>{report.prunedSources.length} sources excluded after checking</summary>
              <ul>{report.prunedSources.map((source) => <li key={source.id}><strong>{source.title}</strong>: {source.prunedReason || 'Unavailable at last check.'}</li>)}</ul>
            </details>
          )}
        </>
      )}
    </section>
  );
};
