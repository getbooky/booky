-- Deleting an account used to leave its e-reader tokens, Kindle addresses,
-- mail settings and reading positions behind, keyed by a bare account id —
-- and account ids are reused, so the next account created could inherit
-- them. Deleting an account now removes them; this clears what earlier
-- deletions left. (Id 0 is the time before accounts existed, and stays.)
DELETE FROM devices          WHERE owner_user_id != 0 AND owner_user_id NOT IN (SELECT id FROM users);
DELETE FROM kindle_devices   WHERE owner_user_id != 0 AND owner_user_id NOT IN (SELECT id FROM users);
DELETE FROM kindle_smtp      WHERE owner_user_id != 0 AND owner_user_id NOT IN (SELECT id FROM users);
DELETE FROM reading_progress WHERE user_id != 0 AND user_id NOT IN (SELECT id FROM users);
DELETE FROM listen_progress  WHERE user_id != 0 AND user_id NOT IN (SELECT id FROM users);
