#!/usr/bin/env bash
# Fetch a kinstack checkout into artifacts/snapshots/kinstack.
#
# Usage:
#   tools/fetch-kinstack.sh <path-or-git-url> [ref]
#
# Env:
#   KINSTACK_SRC  default source when no argument is given
#   KINSTACK_REF  branch/tag used when cloning (default: main)
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DEST="$ROOT/artifacts/snapshots/kinstack"
SRC="${1:-${KINSTACK_SRC:-}}"
REF="${2:-${KINSTACK_REF:-main}}"

if [ -z "$SRC" ]; then
  echo "usage: tools/fetch-kinstack.sh <path-or-git-url> [ref]" >&2
  echo "   or: KINSTACK_SRC=/path/to/kinstack tools/fetch-kinstack.sh" >&2
  exit 1
fi

rm -rf "$DEST"
mkdir -p "$(dirname "$DEST")"

if [ -d "$SRC" ]; then
  echo "copying local checkout: $SRC"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a \
      --exclude node_modules \
      --exclude .git \
      --exclude public/build \
      "$SRC/" "$DEST/"
  else
    mkdir -p "$DEST"
    tar -C "$SRC" \
      --exclude=node_modules \
      --exclude=.git \
      --exclude=public/build \
      -cf - . | tar -C "$DEST" -xf -
  fi
else
  echo "cloning $SRC ($REF)"
  git clone --depth 1 --branch "$REF" "$SRC" "$DEST"
  rm -rf "$DEST/.git"
fi

cat <<EOF

kinstack snapshot: $DEST
apply bench patch:
  cd "$DEST"
  patch -p1 < "$ROOT/docs/BENCH-kinstack.patch"
EOF
