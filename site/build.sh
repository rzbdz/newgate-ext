#!/usr/bin/env bash
# Build the public site: markdown from `site/src` → `site/dist`, plus the clickable UI demo.
#
# # Why this script exists (instead of asking people to type two commands)
#
# The two commands **have an order**, and reversing it breaks things: sitegen wipes all of `site/dist` and rebuilds it
# every time (the reason is in emit in tools/sitegen/main.go -- overwriting leaves deleted pages behind as orphans that only
# search engines can find), and the demo page's output lands exactly in `site/dist/demo`. Build the demo first, then run sitegen,
# and the result is that the demo page is deleted while that link on the site 404s -- **the site itself is fine**, so locally you
# see no problem, and only find out when you click into the demo.
#
# With the order written down in one place, nobody can get it wrong.
#
# # Why it calls node
#
# Only the demo-page half needs it (vite + svelte + codemirror, all in the frontend package.json).
# The docs-only half is dependency-free Go -- so `go run ./tools/sitegen` on its own is always usable offline,
# which is exactly what the "the build must be offline" rule is there to preserve.
#
# Usage:
#
#	bash site/build.sh            # site + demo page → site/dist (with the Pages path prefix)
#	bash site/build.sh --local    # same, but built to "serve from the root", for local preview
#	bash site/build.sh --docs     # site only (leave the frontend toolchain alone)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)

docs_only=0
local=0
for arg in "$@"; do
  case "$arg" in
    --docs) docs_only=1 ;;
    --local) local=1 ;;
    *) echo "Unknown argument: $arg" >&2; exit 2 ;;
  esac
done

# The site generator's base and the demo page's base must be the same value: the demo lives at /demo/ under the site root,
# and when the two disagree the demo's index.html loads but the js it references does not (or the other way round) --
# and in the browser those two cases look almost identical (a blank screen). So there is **exactly one knob**
# (SITEGEN_BASE, the same variable as basePath in tools/sitegen), and the demo section derives from it.
if [ "$local" = "1" ]; then
  export SITEGEN_BASE=/
fi

echo "── sitegen：site/src → site/dist"
(cd "$root" && go run ./tools/sitegen)

if [ "$docs_only" = "1" ]; then
  exit 0
fi

echo "-- demo page: the second entry point of the same frontend → site/dist/demo"
if ! command -v pnpm >/dev/null 2>&1; then
  {
    echo "pnpm not found: the demo page cannot be built."
    echo "The site itself is already built (site/dist), it just has no demo/."
    echo "Install the frontend toolchain (corepack enable pnpm) and run again, or pass --docs to say you only want the docs."
  } >&2
  exit 1
fi
web="$root/modules/web-dashboard/web"
[ -d "$web/node_modules" ] || (cd "$web" && pnpm install)
(cd "$web" && pnpm build:demo)

echo
echo "Site is in $root/site/dist"
echo "View locally: python3 -m http.server -d $root/site/dist 8801"
