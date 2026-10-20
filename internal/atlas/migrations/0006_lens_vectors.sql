-- Lens version two: title vectors for fuzzy and cross-language matching.
-- The objects table reserved an embedding column of 384 dimensions in the
-- baseline; Lens fills it with the title's character n-gram vector
-- (lens.TitleVector), no model needed, and the aliases get one of their
-- own, so a title in another language finds the alias that holds it. A
-- lookup that misses by title key and by text search asks pgvector for the
-- nearest vectors, which the HNSW indexes serve by cosine distance.

ALTER TABLE atlas.aliases ADD COLUMN embedding vector(384);
CREATE INDEX objects_embedding_idx ON atlas.objects USING hnsw (embedding vector_cosine_ops);
CREATE INDEX aliases_embedding_idx ON atlas.aliases USING hnsw (embedding vector_cosine_ops);
