# Reading and Devices

Four ways to read what's on the shelf.

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
whose library access was later revoked. Tokens are stored hashed: only the
plugin zip carries the real one, so a copy of the database can't impersonate
a device. Updating to the version that introduced this signs everyone out of
the web UI once; paired e-readers keep syncing untouched.

## Send to Kindle

Settings → Send to Kindle emails books straight to a Kindle's @kindle.com
address — Amazon accepts EPUB and PDF up to 50 MB. Two pieces, both owned by
your account:

**Outgoing email** — the address your books send from. Any SMTP account
works (for Gmail or iCloud, use an app password, not your real one). This is
required first: there is no shared server account — every person sends from
their own, and until yours is set up the feature is simply off for you. The
password is write-only — stored encrypted, replaceable, never shown again,
admins included.

**Kindle devices** — pair a device with a name and its send-to-kindle
address (find it under Amazon → Manage Your Content & Devices → Preferences →
Personal Document Settings — and while you're there, add your from address to
the **approved sender list**, or Amazon silently drops the mail). Pick which
libraries the device draws from; check **auto-send** on a library and every
new arrival is emailed the moment it imports. Like KoReader devices, everyone
manages their own and an admin sees all of them.

On a book's page, the mail button appears once everything is ready — your
outgoing email set, a device covering that library, and the file in a format
Amazon takes. One device sends on click; several open a picker. Every send
(and failure) is logged to Activity.

Notes:

- Auto-send fires only for *new arrivals* (downloads and manual imports) —
  scanning an existing shelf into Booky never floods a Kindle with backlog.
- Books stored as AZW3/MOBI/CBZ can't be sent; Amazon dropped everything but
  EPUB, PDF and documents for email delivery.
- The encryption key for stored credentials lives in `/config/secret.key`
  (created on first boot, `0600`), or supply your own via the
  `BOOKY_SECRET_KEY` environment variable (64 hex characters). The key is
  deliberately **not** included in backups — a backup zip alone can't yield
  your email password. If you move an install by hand, carry `secret.key`
  along with `booky.db`, or re-enter stored credentials after the move.
