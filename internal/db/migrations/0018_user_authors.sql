-- Authors an account added by hand. Author and series visibility otherwise
-- follows the books: you see an author because one of their books sits in a
-- library you hold. That rule has one gap — "Add author" monitors someone and
-- syncs their bibliography catalog-only, shelving nothing — so without this
-- table an author would vanish from the page of the very user who just added
-- them. Admins are never listed here; they see every author already.
CREATE TABLE user_authors (
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, author_id)
);

CREATE INDEX idx_user_authors_user ON user_authors(user_id);
