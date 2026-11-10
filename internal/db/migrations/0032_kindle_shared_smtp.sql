-- An admin can let everyone send through their outgoing email, so people
-- needn't set up a mail account of their own. At most one row is shared.
ALTER TABLE kindle_smtp ADD COLUMN shared INTEGER NOT NULL DEFAULT 0;
