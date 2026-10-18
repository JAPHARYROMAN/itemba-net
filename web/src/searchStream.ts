import { SearchKnownEvent, SearchMergedEvent, SearchReport } from './types';

export interface SearchStreamHandlers {
  onKnown: (event: SearchKnownEvent) => void;
  onReport: (report: SearchReport) => void;
  onMerged: (event: SearchMergedEvent) => void;
  onDone?: (event: { durationMs: number }) => void;
}

/**
 * Reads the streamed search as server-sent events over fetch, so the same
 * AbortController that cancels a superseded search cancels the stream. An
 * EventSource would reconnect after the server closes the stream and run the
 * search again, which is why it is not used here.
 */
export async function readSearchStream(url: string, signal: AbortSignal, handlers: SearchStreamHandlers): Promise<void> {
  const response = await fetch(url, { signal, headers: { Accept: 'text/event-stream' } });
  if (!response.ok) {
    const detail = await response.json().catch(() => null);
    throw new Error(detail?.error || `Movie search failed (${response.status}). Try again.`);
  }
  if (!response.body) throw new Error('The server did not stream the search. Check the server version.');

  const dispatch = (chunk: string) => {
    let event = 'message';
    const data: string[] = [];
    for (const line of chunk.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim();
      else if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
    }
    if (data.length === 0) return;
    const payload = JSON.parse(data.join('\n'));
    switch (event) {
      case 'known': handlers.onKnown(payload as SearchKnownEvent); break;
      case 'report': handlers.onReport(payload as SearchReport); break;
      case 'merged': handlers.onMerged(payload as SearchMergedEvent); break;
      case 'done': handlers.onDone?.(payload as { durationMs: number }); break;
      default: break;
    }
  };

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary = buffer.indexOf('\n\n');
    while (boundary >= 0) {
      dispatch(buffer.slice(0, boundary));
      buffer = buffer.slice(boundary + 2);
      boundary = buffer.indexOf('\n\n');
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) dispatch(buffer);
}

export function searchStreamUrl(query: string, region: string, fullLengthOnly: boolean): string {
  const params = new URLSearchParams({ query, region, fullLengthOnly: String(fullLengthOnly) });
  return `/api/scout/search/stream?${params.toString()}`;
}
