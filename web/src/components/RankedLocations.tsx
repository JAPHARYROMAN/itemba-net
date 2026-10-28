import React from 'react';
import { Database, Fingerprint, Globe } from 'lucide-react';
import { AtlasEvidence, IdentityMatch, LocationOrigin, SearchKnownEvent, SearchMergedEvent, SearchReport, VerifiedSource } from '../types';
import { identityLabel } from '../identity';
import { MessageKey, useI18n } from '../i18n/core';

interface RankedLocationsProps {
  known: SearchKnownEvent | null;
  merged: SearchMergedEvent | null;
  report: SearchReport | null;
  searching: boolean;
  launchingUrl: string | null;
  onLaunchRbi: (url: string, theaterMode: boolean) => void;
}

// The verification ladder from the white paper, by level.
const LEVEL_HELP: Record<string, MessageKey> = {
  listed: 'level.help.listed',
  reachable: 'level.help.reachable',
  identified: 'level.help.identified',
  rendered: 'level.help.rendered',
  clean: 'level.help.clean',
};
const ACCESS_LABELS: Record<string, MessageKey> = {
  free: 'access.free', subscription: 'access.subscription', rental: 'access.rental', purchase: 'access.purchase', paid: 'access.paid', unknown: 'access.unknown',
};
const ORIGIN_LABELS: Record<LocationOrigin, MessageKey> = { index: 'ranked.origin.index', fresh: 'ranked.origin.fresh', both: 'ranked.origin.both' };

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
  const { t, regionName, formatRelativeTime, formatDateTime } = useI18n();
  const egress = regionName(row.egress, row.egress?.toUpperCase());
  const age = row.ageSeconds === null ? null : formatRelativeTime(row.ageSeconds);
  const check = row.source?.availabilityCheck;
  const playback = row.source?.playbackCheck;
  const help = LEVEL_HELP[row.levelName] ? t(LEVEL_HELP[row.levelName]) : '';
  return (
    <li className={`ladder-card ladder-origin-${row.origin}`}>
      <div className="ladder-card-main">
        <span className={`ladder-badge ladder-level-${row.level}`} title={help}>{row.level} · {row.levelName}</span>
        <div className="ladder-card-title">
          <strong>{row.title || row.url}{row.creator ? <span className="scout-muted"> · {row.creator}</span> : null}</strong>
          <span className="scout-muted">{row.host} · {t(ACCESS_LABELS[row.accessType] ?? 'access.unknown')}</span>
          <span className="scout-muted">
            {age ? t('ranked.checked', { age }) : t('ranked.notChecked')}{egress ? t('ranked.fromEgress', { egress }) : ''} · {t(ORIGIN_LABELS[row.origin])}
          </span>
          {row.identity && (
            <span className={`ladder-identity identity-${row.identity.effect ?? row.identity.verdict ?? 'none'}`} title={row.identity.detail}>
              <Fingerprint size={11} aria-hidden="true" /> {identityLabel(row.identity, t)}{row.identity.peerTitle ? ` · ${row.identity.peerTitle}` : ''}
            </span>
          )}
        </div>
        <button type="button" className="btn btn-primary" disabled={launchingUrl !== null} onClick={() => onLaunchRbi(row.url, false)}>
          <Globe size={14} aria-hidden="true" /> {launchingUrl === row.url ? t('ranked.opening') : t('ranked.open')}
        </button>
      </div>
      <details className="scout-provider-details ladder-evidence">
        <summary>{t('ranked.evidence')}</summary>
        <dl>
          <dt>{t('ranked.level')}</dt><dd>{row.level} {row.levelName}. {help}</dd>
          {row.evidence && (<>
            <dt>{t('ranked.method')}</dt><dd>{row.evidence.method}</dd>
            <dt>{t('ranked.status')}</dt><dd>{row.evidence.status}{row.evidence.detail ? ` · ${row.evidence.detail}` : ''}</dd>
          </>)}
          {!row.evidence && check && (<>
            <dt>{t('ranked.linkCheck')}</dt><dd>{check.method} · {check.status}{check.detail ? ` · ${check.detail}` : ''}</dd>
          </>)}
          {!row.evidence && playback && (<>
            <dt>{t('ranked.playbackSample')}</dt><dd>{playback.method} · {playback.status}{playback.reason ? ` · ${playback.reason}` : ''}</dd>
          </>)}
          {!row.evidence && !check && !playback && (<>
            <dt>{t('ranked.checks')}</dt><dd>{t('ranked.noChecks')}</dd>
          </>)}
          {row.identity && (<>
            <dt>{t('ranked.fingerprint')}</dt><dd>{identityLabel(row.identity, t)}. {row.identity.detail}</dd>
          </>)}
          <dt>{t('ranked.egress')}</dt><dd>{egress ?? t('ranked.unknown')}</dd>
          <dt>{t('ranked.observed')}</dt><dd>{formatDateTime(row.observedAt) ?? t('ranked.notRecorded')}</dd>
          <dt>{t('ranked.location')}</dt><dd className="ladder-url">{row.url}</dd>
        </dl>
      </details>
    </li>
  );
};

export const RankedLocations: React.FC<RankedLocationsProps> = ({ known, merged, report, searching, launchingUrl, onLaunchRbi }) => {
  const { t } = useI18n();
  if (!known && !merged) return null;
  const rows = merged ? rowsFromMerged(merged, known, report) : known ? rowsFromKnown(known) : [];
  const heading = merged
    ? t('ranked.heading.merged', { shown: rows.length })
    : t(rows.length === 1 ? 'ranked.heading.known.one' : 'ranked.heading.known.other', { count: rows.length });
  let note: string | null = null;
  if (merged) note = t('ranked.note.merged', { known: merged.known, fresh: merged.fresh });
  else if (known && !known.configured) note = t('ranked.note.noIndex');
  else if (known && searching) note = t('ranked.note.indexFirst');

  return (
    <section className="ladder-section" aria-label={t('ranked.aria')} aria-busy={searching && !merged}>
      <div className="ladder-header">
        <h3><Database size={14} aria-hidden="true" /> {heading}</h3>
        {note && <span className="scout-muted">{note}</span>}
      </div>
      {known?.error && <p className="scout-muted">{known.error}</p>}
      {rows.length === 0 ? (
        merged
          ? <p className="scout-muted">{t('ranked.empty.merged')}</p>
          : known?.configured ? <p className="scout-muted">{t('ranked.empty.known')}</p> : null
      ) : (
        <ol className="ladder-list">
          {rows.map((row) => <LocationCard key={row.url} row={row} launchingUrl={launchingUrl} onLaunchRbi={onLaunchRbi} />)}
        </ol>
      )}
    </section>
  );
};
