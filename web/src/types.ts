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
  fingerprintCount?: number;
  // renderCheckedCount is how many pictures or documents the isolated
  // sandbox was asked to draw, and renderedCount how many it drew.
  renderCheckedCount?: number;
  renderedCount?: number;
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
  // What an audio sample heard of the file's beginning, or what a video
  // sample saw of it (the frame fingerprint), and how that went: computed,
  // pending, timeout, invalid, failed, too_short, unsupported or the page's
  // own cause.
  fingerprint?: AudioFingerprint;
  fingerprintStatus?: string;
}

// The acoustic fingerprint: band-energy words of the first seconds, four
// bytes a frame, base64.
export interface AudioFingerprint {
  type: string;
  frames: number;
  seconds?: number;
  data: string;
}

// What a fingerprint comparison said about a location: the same sound or
// a different one, the nearest fingerprint it was compared with, and the
// effect on identity: confirmed (same object), joined (another object,
// under different tags), kept_apart (a different sound under the same
// tags). No verdict means nothing to compare with yet.
export interface IdentityMatch {
  method: string;
  verdict: 'same' | 'different' | '';
  effect?: 'confirmed' | 'joined' | 'kept_apart';
  peerUrl?: string;
  peerTitle?: string;
  distance: number;
  seconds?: number;
  detail?: string;
}

// The object types the search form offers.
export type SearchKind = 'video' | 'audio' | 'image' | 'document';

// What a bounded fetch and decode of a picture's file proved: its
// dimensions and format from the header, and the perceptual hash of it.
export interface ImageCheck {
  status: 'decoded' | 'undecodable' | 'unsupported' | 'unreachable' | 'not_checked';
  method: string;
  checkedAt: string;
  detail: string;
  width?: number;
  height?: number;
  format?: string;
  bytes?: number;
  httpStatus?: number;
  evidenceUrl?: string;
  fingerprint?: AudioFingerprint;
  fingerprintStatus?: 'computed' | 'too_large' | 'undecodable' | 'unavailable';
}

// What a bounded fetch and parse of a document's file proved: its format,
// its page count from the file's own page tree when that was within the
// bytes read, and the fingerprint of its text.
export interface DocumentCheck {
  status: 'parsed' | 'undecodable' | 'unsupported' | 'unreachable' | 'not_checked';
  method: string;
  checkedAt: string;
  detail: string;
  format?: string;
  pages?: number;
  pagesConfirmed?: boolean;
  linearized?: boolean;
  bytes?: number;
  httpStatus?: number;
  evidenceUrl?: string;
  textChars?: number;
  textSource?: 'pdf' | 'ocr_text' | 'text_file';
  // bodySkipped is how many words of front matter the fingerprint skipped
  // and bodyWords how many it covers.
  bodySkipped?: number;
  bodyWords?: number;
  fingerprint?: AudioFingerprint;
  fingerprintStatus?: 'computed' | 'too_short' | 'unreadable' | 'partial';
}

// What the isolated sandbox drew of a picture or a document: rendered when
// a picture decoded and its pixels were drawn, or a PDF frame loaded and
// the screenshot shows a drawn page; unconfirmed when it loaded without
// proof of drawing; not_rendered when it could not load.
export interface RenderCheck {
  status: 'rendered' | 'not_rendered' | 'unsupported' | 'unconfirmed' | 'skipped';
  reason?: string;
  method: string;
  country?: string;
  requestedRegion: string;
  checkedAt: string;
  detail: string;
  evidenceUrl?: string;
  width?: number;
  height?: number;
  milliseconds?: number;
  httpStatus?: number;
  mimeType?: string;
  frameLoaded?: boolean;
  visibleShare?: number;
  fingerprint?: AudioFingerprint;
  hashDistance?: number;
  hashAgrees?: 'yes' | 'no';
  // converted says how an e-book or office file became the PDF the
  // sandbox framed: the format, the converter and the time it took.
  converted?: string;
}

// What the conversion route answers with info=1: whether this host can
// convert the format, by which tool, and whether it did.
export interface ConvertStatus {
  url: string;
  format: string;
  supported: boolean;
  converter?: string;
  converters: string[];
  formats: string[];
  converted: boolean;
  cached?: boolean;
  seconds?: number;
  bytes?: number;
  error?: string;
}

// One file of a recording with several, in the host's order.
export interface AudioTrack {
  title: string;
  url: string;
  durationSeconds?: number;
  format?: string;
}

export interface VerifiedSource {
  id: string;
  title: string;
  // kind is the object type the source holds; creator the performer a
  // recording is credited to; tracks the recording's files when it has
  // several. All absent on movie sources.
  kind?: SearchKind;
  creator?: string;
  tracks?: AudioTrack[];
  // license is the licence the host states for the media, as it names it.
  license?: string;
  // imageCheck is what the picture probe proved of a picture's file.
  imageCheck?: ImageCheck;
  // documentCheck is what the document probe proved of a document's file;
  // pages is the count the host states and textUrl the text beside the
  // file, such as the Archive's OCR text.
  documentCheck?: DocumentCheck;
  pages?: number;
  textUrl?: string;
  // renderCheck is what the isolated sandbox drew of the file.
  renderCheck?: RenderCheck;
  // identity is the search's own fingerprint comparison between its
  // samples; the index's verdict arrives in the report's atlas summary.
  identity?: IdentityMatch;
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
  object: { id: number; type: string; title: string; titleKey: string; creator?: string; year?: number; language?: string; contentKey?: string };
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
  identity?: IdentityMatch;
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
  fingerprints?: number;
  // The fingerprint library's verdict on each fingerprinted source, by URL.
  identities?: Record<string, IdentityMatch>;
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
  identity?: IdentityMatch;
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
  // tracks are the other files of a recording with several: the stage
  // queues them after this one so they play in order. collection names the
  // recording they belong to, for the queued tracks' titles.
  tracks?: AudioTrack[];
  collection?: string;
}
