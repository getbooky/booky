-- Per-user library access. A row grants a 'user'-role account one library:
-- what they can browse, add books to, and monitor. Admins are never listed
-- here — they reach every library implicitly, so an admin demoted to user
-- starts with no access rather than inheriting a stale grant.
CREATE TABLE user_libraries (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, library_id)
);

CREATE INDEX idx_user_libraries_user ON user_libraries(user_id);

-- Accounts that predate this table could already reach everything, so grant
-- them every current library instead of silently emptying their view. New
-- accounts get exactly what the admin picks.
INSERT INTO user_libraries (user_id, library_id)
SELECT u.id, l.id FROM users u CROSS JOIN libraries l WHERE u.role = 'user';

-- KoReader devices belong to whoever paired them: a user sees only their own,
-- and the plugin they build can only carry libraries that user may reach.
-- 0 means "paired before accounts existed" — admin-only from here on.
ALTER TABLE devices ADD COLUMN owner_user_id INTEGER NOT NULL DEFAULT 0;
