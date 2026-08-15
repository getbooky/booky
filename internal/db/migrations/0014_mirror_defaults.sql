-- The stock mirror lists moved from code fallbacks into the settings
-- defaults so the UI can show them as removable pills. Under the old code an
-- EMPTY saved value meant "use the built-in defaults"; preserve that meaning
-- once by writing the defaults into empty rows. Rows with custom values are
-- untouched, and rows that never existed pick the defaults up from code.
UPDATE settings
SET value = 'https://annas-archive.gl' || char(10) || 'https://annas-archive.pk' || char(10) || 'https://annas-archive.gd'
WHERE key = 'annas_mirrors' AND TRIM(value) = '';

UPDATE settings
SET value = 'https://z-lib.sk' || char(10) || 'https://z-library.sk' || char(10) || 'https://1lib.sk'
WHERE key = 'zlib_domains' AND TRIM(value) = '';
