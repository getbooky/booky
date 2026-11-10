-- The audiobook profile used to skip "dramatized" and "full cast" releases
-- by default, because a read-along couldn't be lined up with them. It can
-- now (the match check looks at word order, not just how much matched), so
-- the default only skips abridged readings. A profile someone has edited is
-- left exactly as they set it.
UPDATE audiobook_profile SET avoided_terms = 'abridged'
WHERE avoided_terms = 'abridged, dramatized, dramatised, full cast';
