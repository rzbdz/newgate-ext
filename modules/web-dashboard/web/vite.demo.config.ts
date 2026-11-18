import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The **clickable UI demo** on the public site: a second entry into the same front end.
//
// # Why a second entry rather than a second project
//
// ConceptCard statically imports all seven kinds, among which CodeEditor statically
// imports five CodeMirror packages (see package.json) -- static imports cannot be shaken
// out. So "spin up another vite project to import these sources" would require
// **installing the dependencies again** (copying package.json + lockfile, or pulling in a
// pnpm workspace). Copying the whole front-end toolchain for one demo page is the biggest
// waste here.
//
// Sharing one project avoids the problem: one `pnpm install`, the same svelte plugin, the
// same dependency versions. The cost is this file itself (twenty lines of config).
//
// # It cannot get into the product output
//
// outDir points at `site/dist/demo` (under the repo root), while the product path
// (vite.config.ts) reads `web/dist`, and `//go:embed all:web/dist` reads only that one. The
// only intersection between the two is **source**: the demo page imports `../src/...`,
// that is, it runs the very components the product has.
//
// `site/dist` is in .gitignore (same rule as dist/architecture.html: it is the output "as
// of this moment"), so the demo page's output does not go into version control, and thus
// neither of the two Go-side ratchets (web_test.go / i18n_test.go) sees something appear
// out of nowhere.
const here = dirname(fileURLToPath(import.meta.url));

// base is derived from **the site root level**, not written in two places.
//
// Why leave this opening: locally, `python3 -m http.server -d site/dist` serves from the
// **root**, while the Pages site sits under `/newgate-ext/`. Hard-coding would make the
// local demo page a blank screen + a pile of 404s (resources all under a `/newgate-ext/`
// that does not exist) -- and that is exactly the most common way this page gets changed.
//
// Why **derived** rather than a second environment variable: the site generator and the
// demo page must both get their base right at the same time, and they are really the same
// number written down twice (the demo page sits at `/demo/` under the site root). Two
// environment variables leave room for "turned this one, forgot that one", and the symptom
// of that mistake in the browser is a blank screen -- indistinguishable from "the build
// simply did not succeed". So only one opening is kept (`SITEGEN_BASE`, the same variable
// as tools/sitegen's basePath), and the `demo/` segment is appended here:
//
//	SITEGEN_BASE=/ bash site/build.sh --local   # view locally: base becomes /demo/
const SITE_BASE = process.env.SITEGEN_BASE ?? "/newgate-ext/";
const DEMO_BASE = (SITE_BASE.endsWith("/") ? SITE_BASE : SITE_BASE + "/") + "demo/";

export default defineConfig({
  // root is demo/: vite looks for index.html from here. publicDir explicitly points back at
  // the public/ one level up, so the favicon is shared by both entries (by default it would
  // look in demo/public, where there is none).
  root: "demo",
  publicDir: resolve(here, "public"),
  base: DEMO_BASE,
  // The demo page has no backend: the model dropdown must **not** ask /api/ext/modellist
  // (that yields a 404, with "could not ask" on the screen -- and that is not what the demo
  // page is meant to demonstrate). It still lists the manifest declared in providers.json,
  // and the interactions (open, search, grouping) are all playable.
  define: { __NEWGATE_DEMO__: "true" },
  plugins: [svelte()],
  build: {
    outDir: resolve(here, "../../../site/dist/demo"),
    // An outDir outside root is **not cleaned** by default (vite is afraid of deleting the
    // wrong thing), and here "start the whole thing over" is exactly what is wanted: after
    // deleting a line, stale chunks left behind would make the page load the previous
    // version.
    emptyOutDir: true,
    target: "es2022",
    rollupOptions: { output: { manualChunks: undefined } },
  },
});
