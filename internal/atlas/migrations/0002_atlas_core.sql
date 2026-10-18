-- Phase 1 core schema: the records the white paper names in section 8.
-- Objects are canonical things; locations are URLs on hosts that hold them;
-- evidence is one probe result for one location on the verification ladder.
-- Sessions and rules arrive with the phases that produce them.

CREATE TABLE atlas.objects (
    id           bigserial PRIMARY KEY,
    type         text NOT NULL CHECK (type IN ('video', 'audio', 'image', 'document', 'game', 'software', 'live')),
    title        text NOT NULL,
    title_key    text NOT NULL,
    year         integer CHECK (year IS NULL OR year BETWEEN 1800 AND 2200),
    language     text,
    external_ids jsonb NOT NULL DEFAULT '{}'::jsonb,
    search       tsvector GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED,
    -- Reserved for Lens version two: fuzzy and cross-language matching.
    embedding    vector(384),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT objects_identity UNIQUE NULLS NOT DISTINCT (type, title_key, year)
);
CREATE INDEX objects_search_idx ON atlas.objects USING gin (search);
CREATE INDEX objects_title_key_idx ON atlas.objects (title_key);

CREATE TABLE atlas.aliases (
    object_id bigint NOT NULL REFERENCES atlas.objects (id) ON DELETE CASCADE,
    alias     text NOT NULL,
    alias_key text NOT NULL,
    PRIMARY KEY (object_id, alias_key)
);
CREATE INDEX aliases_key_idx ON atlas.aliases (alias_key);

CREATE TABLE atlas.hosts (
    id              bigserial PRIMARY KEY,
    hostname        text NOT NULL UNIQUE,
    provider        text,
    first_seen      timestamptz NOT NULL DEFAULT now(),
    last_seen       timestamptz NOT NULL DEFAULT now(),
    -- Reputation learned only from this system's own history.
    clean_sessions  integer NOT NULL DEFAULT 0,
    broken_sessions integer NOT NULL DEFAULT 0,
    clean_evidence  integer NOT NULL DEFAULT 0
);

CREATE TABLE atlas.locations (
    id          bigserial PRIMARY KEY,
    object_id   bigint NOT NULL REFERENCES atlas.objects (id) ON DELETE CASCADE,
    host_id     bigint NOT NULL REFERENCES atlas.hosts (id),
    url         text NOT NULL UNIQUE,
    media_url   text,
    access_type text NOT NULL DEFAULT 'unknown',
    price       text,
    market      text,
    title       text,
    source_type text,
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX locations_object_idx ON atlas.locations (object_id);
CREATE INDEX locations_host_idx ON atlas.locations (host_id);

CREATE TABLE atlas.evidence (
    id          bigserial PRIMARY KEY,
    location_id bigint NOT NULL REFERENCES atlas.locations (id) ON DELETE CASCADE,
    level       smallint NOT NULL CHECK (level BETWEEN 0 AND 4),
    method      text NOT NULL,
    status      text NOT NULL,
    detail      text,
    egress      text,
    observed_at timestamptz NOT NULL,
    artefacts   jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX evidence_location_observed_idx ON atlas.evidence (location_id, observed_at DESC);
