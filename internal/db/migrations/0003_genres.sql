-- Genres arrive from metadata providers as a JSON string array.
ALTER TABLE books ADD COLUMN genres TEXT NOT NULL DEFAULT '[]';
