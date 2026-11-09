#!/bin/sh
# Bundles scripts/graph-check.ts (esbuild) and runs it with node; stdin/stdout pass through.
set -e
here=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/graph-check.XXXXXX")
trap 'rm -rf "$out"' EXIT
"$here/node_modules/.bin/esbuild" "$here/scripts/graph-check.ts" --bundle --platform=node --format=esm --jsx=automatic --loader:.css=empty --log-level=error --outfile="$out/graph-check.mjs"
node "$out/graph-check.mjs"
