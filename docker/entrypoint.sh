#!/bin/sh
# Drop to the unraid-style PUID/PGID before running the app, so every file
# Booky writes is owned like the rest of the server's media.
set -e

PUID="${PUID:-99}"
PGID="${PGID:-100}"
UMASK="${UMASK:-022}"

umask "$UMASK"

mkdir -p /config
chown "$PUID:$PGID" /config

echo "booky: starting as uid=$PUID gid=$PGID umask=$UMASK"
exec su-exec "$PUID:$PGID" /usr/local/bin/booky
