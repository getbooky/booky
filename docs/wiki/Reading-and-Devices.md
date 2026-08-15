# Reading and Devices

Three ways to read what's on the shelf.

## In the browser

Every book with a file gets a **Read** button — a built-in EPUB reader
(foliate-js) with light and dark page themes. Reading positions are stored
per account and sync across devices, so the phone picks up where the laptop
left off.

## OPDS

Each library exposes its own OPDS feed at `/opds/<library id>`, protected by
HTTP basic auth with that library's credentials (set under Settings →
Libraries — the row shows a ✓ once configured). Works with any OPDS-capable
reader app; the feed serves covers and downloads straight from the shelf.

Library credentials are deliberately separate from user accounts: handing your
e-reader an OPDS login never exposes your Booky password, and rotating one
doesn't touch the other.

## KoReader

Settings → KoReader Devices builds a preconfigured plugin zip per device:

1. Set the **Server URL** (how the device reaches Booky — a Tailscale MagicDNS
   name works great).
2. Pick which libraries the device can browse and which auto-download.
3. Download the zip and drop the `booky.koplugin/` folder into KOReader's
   `plugins/` directory.

The plugin checks in over wifi and pulls new arrivals from the auto-download
libraries onto the device. Devices are listed with their last sync time, and
revoking one kills its token instantly — including devices paired by a user
whose library access was later revoked.
