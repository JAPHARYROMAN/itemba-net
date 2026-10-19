-- Phase 3: audio identity. A recording is its title, its performer and its
-- duration, so objects gain a creator and locations the duration their host
-- listed. The identity constraint widens to the creator key, which is empty
-- for every object recorded so far, so existing rows keep their identity and
-- a video object still conflicts on type, title key and year alone. Both
-- additions are nullable or defaulted, so rows written before this migration
-- need no change.

ALTER TABLE atlas.objects
    ADD COLUMN creator     text,
    ADD COLUMN creator_key text NOT NULL DEFAULT '';
ALTER TABLE atlas.objects DROP CONSTRAINT objects_identity;
ALTER TABLE atlas.objects
    ADD CONSTRAINT objects_identity UNIQUE NULLS NOT DISTINCT (type, title_key, creator_key, year);
CREATE INDEX objects_creator_key_idx ON atlas.objects (creator_key) WHERE creator_key <> '';

ALTER TABLE atlas.locations ADD COLUMN duration_seconds integer;
