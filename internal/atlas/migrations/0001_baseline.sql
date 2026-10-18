-- Phase 0 baseline. The atlas schema and the migrations table are created by
-- the migrator itself before this file runs. This migration only confirms the
-- environment the Phase 1 index will need: the vector extension for fuzzy and
-- cross-language matching of objects and aliases.
CREATE EXTENSION IF NOT EXISTS vector;
