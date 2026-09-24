#!/bin/sh
set -eu

# Exports the public NodeFlow source tree of COMMIT into OUTDIR (which must
# not exist or be empty). Uses `git archive`, so only tracked files of that
# commit are exported, then removes internal working notes that are not part
# of the public repository, and fails when known private markers remain.
#
# usage: [NODEFLOW_EXPORT_DENYLIST=file] scripts/export-public.sh <commit> <outdir>
#
# The orchestrator publishes OUTDIR as one commit on top of the public main
# branch (no private history).

commit=${1:?usage: scripts/export-public.sh <commit> <outdir>}
outdir=${2:?usage: scripts/export-public.sh <commit> <outdir>}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

# Internal working notes: agent goals, design briefs and acceptance contracts
# that reference local screenshots and tooling. Not needed to build or run.
excluded_paths='
GOAL.md
PRODUCT.md
DESIGN.md
.impeccable
docs/frontend-v4-acceptance.md
'

git -C "$root" rev-parse --verify --quiet "$commit^{commit}" >/dev/null \
  || { echo "unknown commit: $commit" >&2; exit 2; }
if [ -e "$outdir" ] && [ -n "$(ls -A "$outdir" 2>/dev/null)" ]; then
  echo "output directory is not empty: $outdir" >&2
  exit 2
fi
mkdir -p "$outdir"
outdir=$(CDPATH='' cd -- "$outdir" && pwd)

git -C "$root" archive --format=tar "$commit" | tar -xf - -C "$outdir"

for path in $excluded_paths; do
  rm -rf -- "${outdir:?}/$path"
done

# Markers that must never reach the public tree: private-network addresses
# and private key bodies, plus one extended regular expression per line from
# the optional NODEFLOW_EXPORT_DENYLIST file (kept outside the repository:
# lab hostnames, owner domains, node UUIDs, lab passwords).
pattern='(^|[^0-9.])192\.168\.[0-9]+\.[0-9]+|PRIVATE KEY-----$'
if [ -n "${NODEFLOW_EXPORT_DENYLIST:-}" ]; then
  [ -r "$NODEFLOW_EXPORT_DENYLIST" ] || { echo "cannot read NODEFLOW_EXPORT_DENYLIST" >&2; exit 2; }
  extra=$(grep -v '^[[:space:]]*\(#\|$\)' "$NODEFLOW_EXPORT_DENYLIST" | paste -sd'|' -)
  [ -z "$extra" ] || pattern="$pattern|$extra"
fi
if findings=$(grep -rIlE -- "$pattern" "$outdir" 2>/dev/null); then
  echo "private markers found in the exported tree:" >&2
  printf '%s\n' "$findings" >&2
  exit 1
fi

files=$(find "$outdir" -type f | wc -l | tr -d ' ')
printf 'exported %s (%s) to %s: %s files\n' "$commit" \
  "$(git -C "$root" rev-parse --short "$commit")" "$outdir" "$files"
printf 'excluded:%s\n' "$(printf '%s' "$excluded_paths" | tr '\n' ' ')"
