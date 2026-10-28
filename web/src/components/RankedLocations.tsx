import React from 'react';
import { Database, Fingerprint, Globe } from 'lucide-react';
import { AtlasEvidence, IdentityMatch, LocationOrigin, SearchKnownEvent, SearchMergedEvent, SearchReport, VerifiedSource } from '../types';
import { identityLabel } from '../identity';
import { formatDateTime, formatRelativeTime, regionName } from '../locale';

interface RankedLocationsProps {
  known: SearchKnownEvent | null;
  merged: SearchMergedEvent | null;
  report: SearchReport | null;
  searching: boolean;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
}

// The verification ladder from the white paper, by level.
const LEVEL_HELP: Record<string, string> = {
  listed: 'A page references the title. Nothing has been checked.',
  reachable: 'The page responded from the check location.',
  identified: 'Media for this title was recognised at the location.',
  rendered: 'A sandbox played it for a bounded sample.',
  clean: 'Played under full protection with no measured ad exposure.',
};
const ACCESS_LABELS: Record<string, string> = {
  free: 'Free', subscription: 'Paid · Subscription', rental: 'Paid · Rent', purchase: 'Paid · Buy', paid: 'Paid', unknown: 'Access unconfirmed',
};
const ORIGIN_LABELS: Record<LocationOrigin, string> = { index: 'from index', fresh: 'fresh discovery', both: 'index + fresh' };

const ageFromIso = (iso?: string): number | null => {
  if (!iso) return null;
  const time = Date.parse(iso);
  return Number.isNaN(time) ? null : Math.max(0, (Date.now() - time) / 1000);
};

interface Row {
  url: string;
  host: string;
  title: string;
  creator?: string;
  level: number;
  levelName: string;
  ageSeconds: number | null;
  observedAt?: string;
  egress?: string;
  accessType: string;
  origin: LocationOrigin;
  evidence?: AtlasEvidence;
  source?: VerifiedSource;
  // What the acoustic fingerprint said about the location, when one was
  // taken: the index's verdict, or the search's own.
  identity?: IdentityMatch;
}

const rowsFromKnown = (known: SearchKnownEvent): Row[] => known.locations.map((k) => ({
  url: k.location.url,
  host: k.host.hostname,
  title: k.location.title || k.object.title,
  creator: k.object.creator,
  level: k.effectiveLevel,
  levelName: k.levelName,
  ageSeconds: k.evidence ? k.ageSeconds : null,
  observedAt: k.evidence?.observedAt,
  egress: k.evidence?.egress,
  accessType: k.location.accessType,
  origin: 'index',
  evidence: k.evidence,
  identity: k.identity,
}));

const rowsFromMerged = (merged: SearchMergedEvent, known: SearchKnownEvent | null, report: SearchReport | null): Row[] => {
  const evidenceByUrl = new Map<string, AtlasEvidence>();
  known?.locations.forEach((k) => { if (k.evidence) evidenceByUrl.set(k.location.url, k.evidence); });
  const sourceByUrl = new Map<string, VerifiedSource>();
  (report?.sources ?? report?.verifiedSources ?? []).forEach((source) => sourceByUrl.set(source.url, source));
  return merged.locations.map((m) => ({
    url: m.url,
    host: m.host,
    title: m.title,
    creator: m.creator,
    level: m.level,
    levelName: m.levelName,
    ageSeconds: ageFromIso(m.observedAt),
    observedAt: m.observedAt,
    egress: m.egress,
    accessType: m.accessType,
    origin: m.origin,
    evidence: evidenceByUrl.get(m.url),
    source: sourceByUrl.get(m.url),
    identity: m.identity,
  }));
};

const LocationCard: React.FC<{ row: Row; launchingUrl: string | null; onLaunchRbi: (url: string, theaterMode: boolean) => void }> = ({ row, launchingUrl, onLaunchRbi }) => {
  const egress = regionName(row.egress, row.egress?.toUpperCase());
  const age = row.ageSeconds === null ? null : formatRelativeTime(row.ageSeconds);
  const check = row.source?.availabilityCheck;
  const playback = row.source?.playbackCheck;
  const help = LEVEL_HELP[row.levelName] ?? '';
  return (
    <li className={`ladder-card ladder-origin-${row.origin}`}>
      <div className="ladder-card-main">
        <span className={`ladder-badge ladder-level-${row.level}`} title={help}>{row.level} · {row.levelName}</span>
        <div className="ladder-card-title">
          <strong>{row.title || row.url}{row.creator ? <span className="scout-muted"> · {row.creator}</span> : null}</strong>
          <span className="scout-muted">{row.host} · {ACCESS_LABELS[row.accessType] ?? ACCESS_LABELS.unknown}</span>
          <span className="scout-muted">
            {age ? `Checked ${age}` : 'Not yet checked'}{egress ? ` from ${egress}` : ''} · {ORIGIN_LABELS[row.origin]}
          </span>
          {row.identity && (
            <span className={`ladder-identity identity-${row.identity.effect ?? row.identity.verdict ?? 'none'}`} title={row.identity.detail}>
              <Fingerprint size={11} aria-hidden="true" /> {identityLabel(row.identity)}{row.identity.peerTitle ? ` · ${row.identity.peerTitle}` : ''}
            </span>
          )}
        </div>
        <button type="button" className="btn btn-primary" disabled={launchingUrl !== null} onClick={() => onLaunchRbi(row.url, false)}>
          <Globe size={14} aria-hidden="true" /> {launchingUrl === row.url ? 'Opening protected browser…' : 'Open with ad blocking'}
        </button>
      </div>
      <details className="scout-provider-details ladder-evidence">
        <summary>Evidence</summary>
        <dl>
          <dt>Level</dt><dd>{row.level} {row.levelName}. {help}</dd>
          {row.evidence && (<>
            <dt>Method</dt><dd>{row.evidence.method}</dd>
            <dt>Status</dt><dd>{row.evidence.status}{row.evidence.detail ? ` · ${row.evidence.detail}` : ''}</dd>
          </>)}
          {!row.evidence && check && (<>
            <dt>Link check</dt><dd>{check.method} · {check.status}{check.detail ? ` · ${check.detail}` : ''}</dd>
          </>)}
          {!row.evidence && playback && (<>
            <dt>Playback sample</dt><dd>{playback.method} · {playback.status}{playback.reason ? ` · ${playback.reason}` : ''}</dd>
          </>)}
          {!row.evidence && !check && !playback && (<>
            <dt>Checks</dt><dd>None recorded. The location is listed by its provider only.</dd>
          </>)}
          {row.identity && (<>
            <dt>Fingerprint</dt><dd>{identityLabel(row.identity)}. {row.identity.detail}</dd>
          </>)}
          <dt>Egress</dt><dd>{egress ?? 'unknown'}</dd>
          <dt>Observed</dt><dd>{formatDateTime(row.observedAt) ?? 'not recorded'}</dd>
          <dt>Location</dt><dd className="ladder-url">{row.url}</dd>
        </dl>
      </details>
    </li>
  );
};

export const RankedLocations: React.FC<RankedLocationsProps> = ({ known, merged, report, searching, launchingUrl, onLaunchRbi }) => {
  if (!known && !merged) return null;
  const rows = merged ? rowsFromMerged(merged, known, report) : known ? rowsFromKnown(known) : [];
  const heading = merged
    ? `Ranked locations · ${rows.length} of the hundred`
    : `From the index · ${rows.length} known location${rows.length === 1 ? '' : 's'}`;
  let note: string | null = null;
  if (merged) note = `${merged.known} from the index · ${merged.fresh} from fresh discovery`;
  else if (known && !known.configured) note = 'No index is configured on this server; results come from fresh discovery only.';
  else if (known && searching) note = 'The index answered first; fresh discovery continues…';

  return (
    <section className="ladder-section" aria-label="Ranked locations" aria-busy={searching && !merged}>
      <div className="ladder-header">
        <h3><Database size={14} aria-hidden="true" /> {heading}</h3>
        {note && <span className="scout-muted">{note}</span>}
      </div>
      {known?.error && <p className="scout-muted">{known.error}</p>}
      {rows.length === 0 ? (
        merged
          ? <p className="scout-muted">No locations ranked for this title.</p>
          : known?.configured ? <p className="scout-muted">Nothing in the index for this title yet. Fresh discovery is running.</p> : null
      ) : (
        <ol className="ladder-list">
          {rows.map((row) => <LocationCard key={row.url} row={row} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} />)}
        </ol>
      )}
    </section>
  );
};
