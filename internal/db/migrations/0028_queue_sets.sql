-- An audiobook posted as separate parts is grabbed as a set: one queue row
-- per part, sharing "<id>/<parts>", imported together once every part is in.
ALTER TABLE queue ADD COLUMN set_id TEXT NOT NULL DEFAULT '';
