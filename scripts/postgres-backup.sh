#!/bin/sh
# Local service backup. Destination directory must be private and absolute.
set -eu
umask 077
case ${1:-} in /*) destination=$1;; *) echo 'usage: postgres-backup.sh /absolute/private/backup-directory' >&2; exit 2;; esac
mkdir -p "$destination"
chmod 700 "$destination"
file="$destination/commander-$(date -u +%Y%m%dT%H%M%SZ)-$$.dump"
tmp="$file.incomplete"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
container=${COMMANDER_POSTGRES_CONTAINER:-commander-postgres}
user=${COMMANDER_POSTGRES_BACKUP_USER:-commander_admin}
database=${COMMANDER_POSTGRES_BACKUP_DATABASE:-commander}
docker exec "$container" pg_dump --username "$user" --dbname "$database" --format custom > "$tmp"
# Validate the archive index before publishing a completed backup.
docker exec -i "$container" pg_restore --list < "$tmp" > /dev/null
mv "$tmp" "$file"
printf '%s\n' "$file"
