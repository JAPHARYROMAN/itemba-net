-- Phase 3: the fingerprint library. A fingerprint is what a probe heard or
-- saw of a location's media, reduced to bytes of a named type: the acoustic
-- band-energy fingerprint of a recording's beginning first, a video's
-- sampled frame sequence and an image's perceptual hash later. One row per
-- location and type; a later observation replaces the earlier. The library
-- also keeps what it concluded when the fingerprint arrived: the nearest
-- stored fingerprint of the type, how far it was, and the verdict and its
-- effect on identity (confirmed, joined, kept_apart), so a lookup can say
-- why a location sits where it does without comparing again.
--
-- Objects gain a content key, empty for everything recorded so far and for
-- every object whose content is unknown or agrees with its tags, so the
-- identity constraint widens without moving a row; a location whose
-- fingerprint disagrees with every fingerprint of the object its tags name
-- becomes an object of its own under a key derived from the fingerprint.
-- The comparison runs in the service rather than the database because it
-- searches alignments, which no index can shift; the bytes are generic.

CREATE TABLE atlas.fingerprints (
    id               bigserial PRIMARY KEY,
    location_id      bigint NOT NULL REFERENCES atlas.locations (id) ON DELETE CASCADE,
    type             text NOT NULL,
    length           integer NOT NULL CHECK (length > 0),
    -- seconds is the span of media the fingerprint covers and
    -- media_seconds the length of the file it was taken from, both as the
    -- probe read them; null where the type is not temporal.
    seconds          real,
    media_seconds    real,
    data             bytea NOT NULL,
    -- variants are the same fingerprint taken at other speeds, when the
    -- type has them: [{speed, frames, data}] with the data in base64.
    variants         jsonb NOT NULL DEFAULT '[]'::jsonb,
    observed_at      timestamptz NOT NULL,
    peer_location_id bigint REFERENCES atlas.locations (id) ON DELETE SET NULL,
    distance         real,
    verdict          text NOT NULL DEFAULT '',
    effect           text NOT NULL DEFAULT '',
    UNIQUE (location_id, type)
);
CREATE INDEX fingerprints_type_observed_idx ON atlas.fingerprints (type, observed_at DESC);

ALTER TABLE atlas.objects ADD COLUMN content_key text NOT NULL DEFAULT '';
ALTER TABLE atlas.objects DROP CONSTRAINT objects_identity;
ALTER TABLE atlas.objects
    ADD CONSTRAINT objects_identity UNIQUE NULLS NOT DISTINCT (type, title_key, creator_key, year, content_key);
