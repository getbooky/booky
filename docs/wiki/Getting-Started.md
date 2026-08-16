# Getting Started

## Install (Docker / unraid)

```
docker run -d --name booky \
  --restart=unless-stopped \
  -p 8787:8787 \
  -e PUID=99 -e PGID=100 -e UMASK=022 \
  -v /mnt/user/appdata/booky:/config \
  -v /mnt/user/data:/data \
  ghcr.io/getbooky/booky:latest
```

Three things matter here:

- **One `/data` mount for everything.** Keep the download client's completed
  folder and every library root under the same mount. That's what makes imports
  hardlink + rename — atomic, instant, no duplicate bytes. Booky warns in the
  UI when a root folder and the download path sit on different filesystems.
- **`--restart=unless-stopped` is not optional.** The in-app Restart button and
  backup-restore both work by exiting and letting Docker bring Booky back.
- **`/config` holds the whole install** — database, settings, cover cache,
  backups, logs. Back that folder up and you can rebuild everything else.

On unraid: Docker tab → Add Container → same repository, port, paths and
variables. PUID 99 / PGID 100 are the unraid defaults; the container drops
root to them.

## First run

Open `http://<server>:8787`. The wizard opens with the one required step —
creating your admin account — then walks through libraries → metadata →
quality profile → sources → SABnzbd → watched lists → e-readers → Send to
Kindle. Everything after the account is skippable, per-step or entirely —
wizard fields are ordinary settings and live in Settings afterwards. The
wizard only appears on its own while no account exists; admins can re-run it
from Settings → About.

## Accounts

Creating the admin account is the wizard's first, mandatory step: the API is
open only for that brief first-run window and locks the moment the account
exists. Login is rate-limited, and nothing usable is ever stored raw:
passwords are bcrypt-hashed, session and device tokens are stored as hashes,
and credentials Booky must present elsewhere — SMTP passwords for Send to
Kindle, provider tokens and API keys — are write-only (never shown again in
the UI or API) and encrypted under a key kept outside the database. A stolen
database or backup yields none of them. OPDS feeds and KoReader devices use
their own per-library credentials, never your account — see
[Reading and Devices](Reading-and-Devices.md).

Roles and per-library access are covered in
[Libraries and Users](Libraries-and-Users.md).

## Remote access

Booky pairs well with Tailscale: run the container as a tailnet node (it
listens on 8787) and reach it by MagicDNS name from your phone. The web app is
an installable PWA — add it to your home screen and you get bottom navigation,
denser grids, and safe-area-aware layout.
