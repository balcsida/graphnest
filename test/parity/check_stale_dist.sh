#!/bin/sh
# Live regression check; requires the same dependency access as regeneration.
set -eu
upstream=$1
node=${2:-node}
temporary=$(mktemp -d "${TMPDIR:-/tmp}/graphnest-stale-producer.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
git clone --quiet --shared "$upstream" "$temporary/producer"
git -C "$temporary/producer" checkout --quiet --detach b9ca4b7981116909900368cc1686a1074cd4d4c1
mkdir -p "$temporary/producer/dist"
printf '%s\n' 'throw new Error("STALE_DIST_MUST_NOT_EXECUTE");' > "$temporary/producer/dist/index.js"
CODEGRAPH_VALUE_REFS=1 CODEGRAPH_NO_REBIND=1 CODEGRAPH_KERNEL=1 CODEGRAPH_EXTRACT_WORKERS=8 \
  python3 "$(dirname "$0")/generate_reference.py" --upstream "$temporary/producer" --node "$node" --check
