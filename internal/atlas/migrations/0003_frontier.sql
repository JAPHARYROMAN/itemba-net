-- Phase 2: the crawl frontier. Each row is one URL the crawler owes a fetch:
-- a driver search for an object (kind search) or a known location to probe
-- again (kind page). Rows are leased while a worker holds them so several
-- crawler processes can share one frontier.

CREATE TABLE atlas.frontier (
    id              bigserial PRIMARY KEY,
    kind            text NOT NULL CHECK (kind IN ('search', 'page')),
    driver          text,
    object_id       bigint REFERENCES atlas.objects (id) ON DELETE CASCADE,
    location_id     bigint REFERENCES atlas.locations (id) ON DELETE CASCADE,
    host            text NOT NULL,
    url             text NOT NULL UNIQUE,
    market          text,
    priority        integer NOT NULL DEFAULT 0,
    tier            smallint NOT NULL DEFAULT 1 CHECK (tier BETWEEN 1 AND 3),
    attempts        integer NOT NULL DEFAULT 0,
    next_fetch_at   timestamptz NOT NULL DEFAULT now(),
    leased_until    timestamptz,
    last_status     text,
    last_fetched_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX frontier_due_idx ON atlas.frontier (next_fetch_at, priority DESC);
CREATE INDEX frontier_host_idx ON atlas.frontier (host);
CREATE INDEX frontier_object_idx ON atlas.frontier (object_id);
