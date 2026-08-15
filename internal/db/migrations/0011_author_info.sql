-- Author presentation data pulled from Hardcover: portrait + biography.
-- image_url is the provider's source URL; the served photo is cached on disk
-- (like book covers) so browsing never triggers external requests.
ALTER TABLE authors ADD COLUMN bio TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN image_url TEXT NOT NULL DEFAULT '';
ALTER TABLE authors ADD COLUMN info_synced_at TEXT;
