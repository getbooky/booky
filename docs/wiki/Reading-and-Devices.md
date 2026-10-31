# Reading and Devices

Four ways to read what's on the shelf.

## In the browser

Every book with a file gets a **Read** button — a built-in EPUB reader
(foliate-js) with light and dark page themes. Reading positions are stored
per account and sync across devices, so the phone picks up where the laptop
left off.

## OPDS

Every account has an OPDS catalog at `/opds` — the exact address is under
**Settings → Account**, with a copy button. Add it to any OPDS reader
(KOReader's OPDS browser, Moon+ Reader, Thorium…) and sign in with your usual
Booky username and password.

The catalog shows exactly your books — the ones shared with your tags plus
your own — and lets you browse **All books**, **Recently added**, **Authors**,
**Series** and your **Tags**, or **search** by title, author or series (apps
that support OpenSearch show a search box). Long lists page at 100 books.
Every download and cover is checked against your tags again, so a link to
someone else's book is a "not found". Five wrong passwords lock that login
from that address for a short while.

The per-library feeds of earlier versions (`/opds/<library id>`) are gone and
answer with a note pointing at `/opds`.

## KoReader

Settings → KoReader Devices builds a preconfigured plugin zip per device:

1. Set the **Server URL** (how the device reaches Booky — a Tailscale MagicDNS
   name works great).
2. Pick which of your tags the device carries and which auto-download.
3. Download the zip and drop the `booky.koplugin/` folder into KOReader's
   `plugins/` directory.

The plugin checks in over wifi, lets you browse by author, series and genre,
and pulls new arrivals with an auto-download tag onto the device. A device
never reaches past its owner: if a tag is taken away from someone, their
devices stop seeing those books on the next sync. Devices are listed with
their last sync time, and revoking one kills its token instantly. Plugins
installed before tags replaced libraries keep working without a rebuild. Tokens are stored hashed: only the
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
the **approved sender list**, or Amazon silently drops the mail). Pick the
tags it **auto-sends**: a book is emailed once, the first moment it's both on
disk and carrying one of them — when it imports, or when it's tagged later —
and only while the device's owner holds that tag. Like KoReader devices,
everyone manages their own and an admin sees all of them.

On a book's page, the mail button appears once everything is ready — your
outgoing email set, a device, and the file in a format Amazon takes. Manual
sends take anything the device's owner can see. One device sends on click;
several open a picker. Every send (and failure) is logged to Activity.

Notes:

- Each book goes to each device once — a send log makes sure of it.
  Upgrading from libraries marks everything already auto-sent as sent.
- Adding an auto-send tag to a device doesn't email the books that already
  carry it, and tagging a batch asks before sending more than a handful.
- Scanning an existing shelf into Booky never floods a Kindle with backlog.
- Books stored as AZW3/MOBI/CBZ can't be sent; Amazon dropped everything but
  EPUB, PDF and documents for email delivery.
- The encryption key for stored credentials lives in `/config/secret.key`
  (created on first boot, `0600`), or supply your own via the
  `BOOKY_SECRET_KEY` environment variable (64 hex characters). The key is
  deliberately **not** included in backups — a backup zip alone can't yield
  your email password. If you move an install by hand, carry `secret.key`
  along with `booky.db`, or re-enter stored credentials after the move.
