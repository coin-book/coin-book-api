-- Searching ignores diacritics: unaccent() is applied to both sides of the match.
-- The extension is trusted, so the schema owner may create it without superuser rights.
CREATE EXTENSION IF NOT EXISTS unaccent;
