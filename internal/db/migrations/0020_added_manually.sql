-- Separate "somebody asked for this author" from "this author is tracked".
--
-- The two used to be the same flag. authors.monitored was a switch you could
-- turn off, so an author whose books you removed could be dismissed from the
-- Authors page by hand. Making tracking automatic (a book in a library earns
-- it, nothing turns it off) left the flag stuck on forever — and the page's
-- visibility rule was keyed to it, so an author or series that had EVER been
-- shelved stayed listed even after every last book was removed.
--
-- Visibility now follows the books: an author or series is on those pages
-- while something of theirs is in a library. The one exception is an author
-- added by hand, whose whole point is a bibliography with nothing shelved yet
-- — and that is what this column records.
ALTER TABLE authors ADD COLUMN added_manually INTEGER NOT NULL DEFAULT 0;

-- user_authors has recorded hand-adds since migration 0018, for admins as
-- well as scoped users, so it can seed this exactly for anything added since.
-- Older rows can't be told apart from leftovers and stay 0 — which is the
-- answer that cleans up the authors this bug stranded.
UPDATE authors SET added_manually = 1
WHERE EXISTS (SELECT 1 FROM user_authors ua WHERE ua.author_id = authors.id);
