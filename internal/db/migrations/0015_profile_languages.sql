-- The quality_profiles.language column shipped in 0001 with default 'en' but
-- was never read or editable. It now holds the profile's accepted release
-- languages (newline/comma-separated names) driving the language filter, so
-- normalize the legacy placeholder to the canonical spelling the UI shows.
-- An intentionally emptied list ('' after a user clears it) means the filter
-- is off; only untouched legacy values are migrated.
UPDATE quality_profiles SET language = 'english' WHERE TRIM(language) IN ('', 'en');
