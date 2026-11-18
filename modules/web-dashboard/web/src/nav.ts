// Translation between a position (which section, which card) and the URL. **Pure
// functions**, holding no state.
//
// # Why a hash instead of path segments
//
// `/ui/config` would actually work (the BFF serves index.html for every GET whose basename
// has no dot, so the SPA fallback already supports it), so this is not "paths do not
// work". The reason is **don't let that bug grow back**: resolving a relative address
// depends on the current document's path -- with the document inside
// `/ui/s/config/xxx`, `api/snapshot` resolves to `/ui/s/config/api/snapshot`, falls out of
// the mount point and lands in the data plane. The runtime address is absolute now (see
// api.ts's API), but the hash keeps the document address **always only one segment deep**,
// so that coupling never gets a chance to grow back.
//
// Secondary benefit: changing the hash sends no request, one `hashchange` listener is
// enough (pushState + popstate would need its own patch-up as well).
//
// # Why the route key is source, not the column name
//
// The column name is **human-facing and translated**. Use it as the route key and the
// Chinese UI and the English UI become two sets of URLs, and changing a wording kills
// every bookmark. source is a machine tag (module name), not translated and not changed.

/**
 * "What is being looked at right now" on the UI.
 *
 * # Two modes
 *
 * `home` true = **home mode**: only the first screen section's cards, no table of
 * contents, no column bar, no card tab strip. This is the mode **you land in on open** (the
 * user's own words: "most of the time I just use the home page"), and its address is the
 * one **without `#/s/…`** (`/ui/`).
 *
 * `home` false = **full UI**: table of contents + sections + all controls. `section` empty
 * string = not decided yet (App picks one for the first screen).
 *
 * Two things in two fields rather than deriving from "is section the landing section":
 * that way clicking "home" inside the full UI would collapse the table of contents on the
 * spot, when the user only wanted to look at that section in the full UI.
 */
export interface Route {
  /** Home mode (see above). Mutually exclusive with section. */
  home: boolean;
  /** Source (module name) -- machine tag, not translated. */
  section: string;
  /** Concept ID. Empty string = the first one in that section. */
  card: string;
  /** Whether to show the two halves (controls on the left / raw text on the right) side
      by side. Collapsed means showing only the controls half. */
  split: boolean;
}

export const emptyRoute: Route = { home: false, section: "", card: "", split: true };

/** The home-mode position (address is `/ui/`, without `#/s/…`). */
export const homeRoute: Route = { home: true, section: "", card: "", split: true };

/**
 * Parse location.hash into a position. Anything unrecognized is treated as "not decided
 * yet" -- **no error**: hand-typed URLs, hashes left by older versions, links truncated by
 * a chat app should all land on the home page rather than a blank screen.
 */
export function parseHash(hash: string): Route {
  const body = hash.replace(/^#\/?/, "");
  const [path, query] = body.split("?");
  const parts = path.split("/");
  // Anything without `s/<section>` is **home mode** -- an empty hash, `#/`, a typo all
  // land here. The "land on the home page when unrecognized" rule was always there; home
  // mode just makes it land somewhere nicer.
  if (parts[0] !== "s" || !parts[1]) return { ...homeRoute };
  return {
    home: false,
    section: decodeURIComponent(parts[1]),
    // The concept ID **contains slashes itself** (config.file.mappings/demo.json) -- so
    // split once and stop; the whole rest is the ID. Splitting on every "/" and taking the
    // third segment would cut that ID down to config.file.mappings, and after a refresh
    // you land back on the first card of the first section (looks like "the bookmark does
    // not work").
    card: parts.slice(2).map(decodeURIComponent).join("/"),
    split: new URLSearchParams(query ?? "").get("split") !== "0",
  };
}

/** Position -> hash fragment (with the leading #). */
export function routeTo(r: Route): string {
  // Home mode is written as the **empty string** (not even a `#` is left in the address
  // bar): `/ui/` is its natural shape, and writing a `#/` would push a history entry out
  // of nowhere on open (browsers treat "no fragment" and "empty fragment" as two
  // addresses), so the first press of "back" appears to do nothing -- see the note under
  // writeHash below.
  if (r.home) return "";
  const card = r.card ? "/" + r.card.split("/").map(encodeURIComponent).join("/") : "";
  return `#/s/${encodeURIComponent(r.section)}${card}${r.split ? "" : "?split=0"}`;
}

/**
 * writeHash writes the position into the address bar. **Only when it really differs**:
 * writing an identical one pushes a history entry, so "back" has to be pressed several
 * times to move (and each press leaves you on the same screen).
 */
export function writeHash(r: Route): void {
  const next = routeTo(r);
  if (location.hash === next) return;
  // Home mode = the empty string, while "no fragment" in the address bar writes as `/ui/`
  // and assigning an empty hash writes `/ui/#`. The two are the same place, so nothing is
  // written here -- otherwise opening the page adds one history entry, and pressing "back"
  // on that entry returns to the same screen (looks like the back key is broken).
  if (next === "" && location.hash === "") return;
  location.hash = next;
}

/**
 * fileOf says "which file this concept writes", relative to the config root.
 *
 * This is **a convention**, not derived from Kind: it works because both Kinds' data
 * shapes already carry the path of their own file (`mapping-editor`/`toggles` have `file`,
 * `code` has `path`) -- those are fields that had to be known to render them anyway. The
 * same relative path = the same file.
 *
 * It knows no module: the word `config` does not appear here once (which is exactly what
 * "the front end does not know modules" means). Should a third Kind ever need to sit side
 * by side, add a field name here.
 */
export function fileOf(c: { kind: string; data: unknown }): string | undefined {
  const d = c.data as { file?: unknown; path?: unknown } | null;
  const v = d?.file ?? d?.path;
  return typeof v === "string" && v ? v : undefined;
}

/**
 * The **meaning** of one shortcut press (not the key itself).
 *
 * Why separate: the key mapping lives in Shortcuts.svelte (which also handles "when not
 * to accept"), while "what happens after the press" lives in App (only it knows which
 * sections and cards there are). A semantic layer in between exists because one meaning
 * may later map to several keys (`]` and `j` both mean "next card"), and such a change
 * must not have to touch App.
 */
export type Action =
  | { kind: "save" }
  | { kind: "focus-filter" }
  | { kind: "blur" }
  | { kind: "card"; delta: 1 | -1 }
  | { kind: "section"; index: number };

/**
 * shortcutAllowed decides whether this key press should be taken by the UI.
 *
 * Single-key shortcuts (`[`, `]`, `/`) are **ordinary characters** inside an input box.
 * Taking them without checking means the user cannot type a square bracket while editing
 * a config file -- and that is one of the hardest failures to associate with a shortcut.
 * CodeMirror's editor is part of the criterion too: it is a contenteditable neighbor, and
 * that `.cm-editor` selector is its own container.
 */
export function shortcutAllowed(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.closest !== "function") return true;
  return !el.closest("input, textarea, select, [contenteditable='true'], .cm-editor");
}
