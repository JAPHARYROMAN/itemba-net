export type ThreatLayer =
  | 'L1_DNS_SINKHOLE'
  | 'L2_CDP_FILTER'
  | 'L3_DOM_SWEEPER'
  | 'L3_POPUP_GUILLOTINE';

export interface ThreatEvent {
  id: number;
  timestamp: string;
  layer: ThreatLayer;
  category: string;
  target: string;
  rule: string;
  listSource: string;
  resourceType?: string;
}

export interface SniffedStream {
  url: string;
  format: string;
  resolution?: string;
  discoveredAt: string;
  sourceLayer: string;
}

export interface EngineStats {
  dnsSinkholeDomains: number;
  cdpFilterRules: number;
  domCosmeticRules: number;
  exceptionRules: number;
  activeBlocklists: string[];
  lastSyncedAt: string;
}

export interface TelemetrySnapshot {
  totalBlocked: number;
  totalAllowed: number;
  layer1DnsCount: number;
  layer2CdpCount: number;
  layer3DomCount: number;
  layer3PopupCount: number;
  engineStats: EngineStats;
  sniffedStreams: SniffedStream[];
  recentEvents: ThreatEvent[];
}

export interface MovieAvailabilityCheck {
  status: 'media_reachable' | 'page_reachable' | 'region_blocked' | 'not_found' | 'sign_in_required' | 'access_restricted' | 'unconfirmed' | 'error';
  country?: string;
  requestedRegion?: string;
  checkedAt: string;
  detail: string;
  httpStatus?: number;
  evidenceUrl?: string;
  playbackConfirmed: boolean;
  method: string;
}

export interface SearchAvailabilityChecks {
  country?: string;
  requestedRegion: string;
  checkedAt: string;
  method: string;
  checkedCount: number;
  detail: string;
  playbackCheckedCount?: number;
  playbackStartedCount?: number;
  providerCheckedCount?: number;
}

export interface MoviePlaybackCheck {
  status: 'started' | 'not_started' | 'unsupported' | 'unconfirmed' | 'skipped' | 'expired';
  reason?: 'region_unavailable' | 'sign_in_required' | 'drm_required' | 'content_unconfirmed' | 'trailer_only' | 'blocked_with_protection' | 'timeout' | 'ads_only' | 'expired' | 'protection_changed' | 'location_changed';
  country?: string;
  requestedRegion: string;
  checkedAt?: string;
  detail: string;
  evidenceUrl?: string;
  secondsAdvanced?: number;
  decodedFrames?: number;
  width?: number;
  height?: number;
  durationSeconds?: number;
  method: string;
  playbackConfirmed: boolean;
}

// The object types the search form offers. Images and documents arrive with
// their Phase 3 slices.
export type SearchKind = 'video' | 'audio';

export interface VerifiedSource {
  id: string;
  title: string;
  // kind is the object type the source holds; creator the performer a
  // recording is credited to. Both are absent on movie sources.
  kind?: SearchKind;
  creator?: string;
  url: string;
  directStreamUrl?: string;
  provider: string;
  sourceType: string;
  description: string;
  thumbnail?: string;
  duration?: string;
  resolution?: string;
  verified: boolean;
  prunedReason?: string;
  hasVideoDom: boolean;
  hasIframePlayer: boolean;
  detectedManifests: string[];
  streamFormat: string;
  cleanlinessScore: number;
  threatsBlocked: number;
  verifyDurationMs: number;
  verificationMode?: string;
  accessType?: 'free' | 'subscription' | 'rent' | 'buy' | 'paid' | 'unknown';
  accessNote?: string;
  price?: string;
  region?: string;
  releaseYear?: number;
  listingOnly?: boolean;
  verificationStatus?: 'listing' | 'page_reachable' | 'media_reachable' | 'unavailable';
  checkedAt?: string;
  relevanceScore?: number;
  destinationType?: 'watch' | 'guide' | 'unknown';
  accessBasis?: 'catalog' | 'provider_model' | 'page_text' | 'unknown' | 'archive_metadata' | 'peertube_api' | 'creator_catalog' | 'filmzie_movie_metadata' | 'nasa_movie_metadata' | 'creator_movie_metadata' | 'fawesome_movie_metadata' | 'nfb_movie_metadata';
  accessEvidenceUrl?: string;
  availabilityStatus?: 'regional_listing' | 'service_available' | 'unknown' | 'other_region' | 'unavailable';
  availabilityNote?: string;
  availabilityEvidenceUrl?: string;
  availabilityCheck?: MovieAvailabilityCheck;
  playbackCheck?: MoviePlaybackCheck;
}

export interface SearchProviderStatus {
  name: string;
  status: 'ok' | 'empty' | 'error' | 'not_configured';
  detail: string;
}

export type MovieDiscoveryMode = 'popular' | 'recent';

export interface MovieDiscoveryItem {
  id: string;
  title: string;
  releaseDate?: string;
  catalogDate?: string;
  query: string;
  thumbnail?: string;
  sourceUrl: string;
  sourceName: string;
  markets: string[];
  accessType?: 'free';
  playbackState?: 'queued' | 'checking' | 'checked' | 'expired' | 'unchecked';
  playbackCheck?: MoviePlaybackCheck;
  playbackExpiresAt?: string;
}

export interface FawesomeShelfPlaybackReport {
  jobId: string;
  mode: MovieDiscoveryMode;
  region: 'ALL';
  adBlockingEnabled: boolean;
  state: 'running' | 'complete' | 'cancelled' | 'expired';
  items: MovieDiscoveryItem[];
  total: number;
  checkedCount: number;
  startedCount: number;
  pendingCount: number;
  checkingCount: number;
  checkedAt: string;
  expiresAt: string;
  pollAfterMs: number;
  detail: string;
}

export interface MovieDiscoveryReport {
  mode: MovieDiscoveryMode;
  region: string;
  checkedAt: string;
  cached?: boolean;
  detail?: string;
  items: MovieDiscoveryItem[];
  providers: SearchProviderStatus[];
}

export interface SearchReport {
  query: string;
  kind?: SearchKind;
  totalCandidates: number;
  verifiedCount: number;
  prunedCount: number;
  durationMs: number;
  verifiedSources: VerifiedSource[];
  prunedSources: VerifiedSource[];
  sources?: VerifiedSource[];
  listedCount?: number;
  region?: string;
  fullLengthOnly?: boolean;
  providers?: SearchProviderStatus[];
  availabilityChecks?: SearchAvailabilityChecks;
  atlas?: AtlasRecordSummary;
  atlasError?: string;
}

// The streamed search: GET /api/scout/search/stream sends known (from the
// index), report (fresh discovery), merged (the ranked hundred) and done.

export interface ObjectQuery {
  raw: string;
  type: string;
  title: string;
  titleKey: string;
  rawKey?: string;
  creator?: string;
  creatorKey?: string;
  year?: number;
  url?: string;
  hash?: string;
}

export interface AtlasEvidence {
  level: number;
  method: string;
  status: string;
  detail?: string;
  egress?: string;
  observedAt: string;
  artefacts?: Record<string, unknown>;
}

export interface KnownLocation {
  object: { id: number; type: string; title: string; titleKey: string; creator?: string; year?: number; language?: string };
  host: { id: number; hostname: string; provider?: string };
  location: {
    id: number;
    objectId: number;
    hostId: number;
    url: string;
    mediaUrl?: string;
    accessType: string;
    price?: string;
    market?: string;
    title?: string;
    sourceType?: string;
    lastSeen: string;
  };
  evidence?: AtlasEvidence;
  effectiveLevel: number;
  levelName: string;
  ageSeconds: number;
  relevance: number;
}

export interface SearchKnownEvent {
  configured: boolean;
  query: ObjectQuery;
  locations: KnownLocation[];
  error?: string;
}

export interface AtlasRecordSummary {
  objects: number;
  locations: number;
  evidence: number;
  skipped?: number;
}

export type LocationOrigin = 'index' | 'fresh' | 'both';

export interface MergedLocation {
  url: string;
  host: string;
  title: string;
  creator?: string;
  level: number;
  levelName: string;
  observedAt?: string;
  egress?: string;
  accessType: string;
  origin: LocationOrigin;
  relevance: number;
}

export interface SearchMergedEvent {
  query: ObjectQuery;
  locations: MergedLocation[];
  known: number;
  fresh: number;
}

export interface VaultStatus {
  active: boolean;
  sessionId?: string;
  mode?: string;
  containerId?: string;
  currentUrl?: string;
  pageTitle?: string;
  theaterActive: boolean;
  webrtcState: string;
  framesStreamed: number;
  uptimeSeconds: number;
  extractedStreams: SniffedStream[];
  adBlockingEnabled?: boolean;
}

// What the console presents a relayed location as. Video and audio go to
// the playback engine, images to the zoomable viewer and documents to the
// reader; 'video' is the default when a URL gives nothing away.
export type MediaKind = 'video' | 'audio' | 'image' | 'document';

export interface SubtitleTrack {
  label: string;
  lang?: string;
  url: string;
}

export interface ActiveRelayStream {
  title: string;
  streamUrl: string;
  pageUrl?: string;
  provider: string;
  format: string;
  resolution?: string;
  useZeroTrustProxy: boolean;
  // Optional presentation hints. kind is detected from the URL and format
  // when absent; subtitles are side-loaded WebVTT files the source publishes.
  kind?: MediaKind;
  poster?: string;
  subtitles?: SubtitleTrack[];
  durationSeconds?: number;
}
