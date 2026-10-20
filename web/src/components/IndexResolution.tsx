import React, { useEffect, useState } from 'react';
import { Compass } from 'lucide-react';
import { KIND_LABELS } from '../media/media';

// What the index takes the words to mean, from /api/atlas/resolve: Lens
// version two's reading of the query (its kind, or open to every kind, its
// title and creator) and the objects Atlas ranks for it, with how each was
// found: by its title key, an alias, the text search, or the nearest title
// vector of the object or of an alias, which is what finds a misspelling
// or a title in another language.

interface ResolvedQuery {
  type: string;
  open?: boolean;
  title: string;
  creator?: string;
  year?: number;
}

interface ResolvedObject {
  object: { id: number; type: string; title: string; creator?: string; year?: number };
  score: number;
  method: 'title' | 'alias' | 'search' | 'vector' | 'alias_vector';
  alias?: string;
}

interface ResolveResponse {
  query: ResolvedQuery;
  matches: ResolvedObject[];
  error?: string;
}

const METHOD_LABELS: Record<ResolvedObject['method'], string> = {
  title: 'by title',
  alias: 'by alias',
  search: 'by text search',
  vector: 'by title vector',
  alias_vector: 'by alias vector',
};

const kindLabel = (kind: string): string => {
  switch (kind) {
    case 'video': return KIND_LABELS.video;
    case 'audio': return KIND_LABELS.audio;
    case 'image': return 'Picture';
    case 'document': return KIND_LABELS.document;
    default: return kind;
  }
};

export const IndexResolution: React.FC<{ query: string }> = ({ query }) => {
  const [response, setResponse] = useState<ResolveResponse | null>(null);

  useEffect(() => {
    if (!query.trim()) {
      setResponse(null);
      return undefined;
    }
    const controller = new AbortController();
    void fetch(`/api/atlas/resolve?q=${encodeURIComponent(query.trim())}`, { signal: controller.signal })
      .then(async (res) => setResponse((await res.json()) as ResolveResponse))
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === 'AbortError') return;
        setResponse(null);
      });
    return () => controller.abort();
  }, [query]);

  if (!response) return null;
  const { query: q, matches, error } = response;
  const reading = `${q.open ? 'any kind' : kindLabel(q.type).toLowerCase()}${q.title ? ` · “${q.title}”` : ''}${q.creator ? ` by ${q.creator}` : ''}${q.year ? ` · ${q.year}` : ''}`;
  return (
    <div className="index-resolution" role="status">
      <p className="scout-muted"><Compass size={12} aria-hidden="true" /> The index reads this as {reading}.{error ? ` ${error}` : matches.length === 0 ? ' Nothing in the index resolves it yet.' : ''}</p>
      {matches.length > 0 && (
        <ul className="index-resolution-list">
          {matches.slice(0, 5).map((m) => (
            <li key={m.object.id} className="index-resolution-item">
              <span className="pill-badge">{kindLabel(m.object.type)}</span>
              <strong>{m.object.title}</strong>
              {m.object.creator ? <span className="scout-muted"> · {m.object.creator}</span> : null}
              {m.object.year ? <span className="scout-muted"> ({m.object.year})</span> : null}
              <span className="scout-muted"> · {Math.round(m.score * 100)}% {METHOD_LABELS[m.method] ?? m.method}{m.alias ? ` “${m.alias}”` : ''}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
};
