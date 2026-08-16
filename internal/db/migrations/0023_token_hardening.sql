-- Bearer tokens join the "nothing usable in a stolen database" guarantee.
-- Web sessions are stored as SHA-256 hashes from now on (the cookie carries
-- the raw token; lookups hash it) — existing rows hold raw tokens that can't
-- match a hash, so they're dropped: one forced re-login per person.
DELETE FROM sessions;

-- KoReader device tokens become a SHA-256 lookup hash in `token`, with a
-- sealed (AES-GCM) copy for rebuilding plugin zips. Legacy rows still hold
-- the raw token and NULL ct — converted in place at startup once the secret
-- keeper is loaded, so paired devices keep syncing without a re-pair.
ALTER TABLE devices ADD COLUMN token_ct BLOB;
