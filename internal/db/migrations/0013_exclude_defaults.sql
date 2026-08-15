-- The built-in compilation keywords moved from code into the
-- exclude_patterns setting so users can remove individual terms. Installs
-- that already wrote the key (any saved exclusion list) get the defaults
-- appended once; installs that never wrote it pick the new default up from
-- code. Idempotent by name: this migration runs once.
UPDATE settings
SET value = CASE
    WHEN TRIM(value) = '' THEN 'box set' || char(10) || 'boxed set' || char(10) || 'omnibus' || char(10) || 'collection' || char(10) || 'bundle' || char(10) || 'anthology' || char(10) || 'complete series' || char(10) || 'trilogy'
    ELSE value || char(10) || 'box set' || char(10) || 'boxed set' || char(10) || 'omnibus' || char(10) || 'collection' || char(10) || 'bundle' || char(10) || 'anthology' || char(10) || 'complete series' || char(10) || 'trilogy'
END
WHERE key = 'exclude_patterns';
