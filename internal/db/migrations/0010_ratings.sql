-- Provider popularity stat (Goodreads ratings count / Hardcover users count,
-- whichever is larger). Display-only: refreshed from providers, never locked.
ALTER TABLE books ADD COLUMN ratings_count INTEGER NOT NULL DEFAULT 0;
