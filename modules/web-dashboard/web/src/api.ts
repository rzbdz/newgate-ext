// BFF client. **Only these four things**: contract version, snapshot, save, health.
//
// It deliberately knows nothing about profiles, the gateway or counters -- it only knows
// the **shape** of a concept (Kind). Adding one module's UI does not require touching this
// file, just as adding one module's UI does not require touching the BFF: the front-end
// half of that rule is this file.

export type Kind =
  | "mapping-editor"
  | "code"
  | "toggles"
  | "records"
  | "series"
  | "table"
  | "log"
  | "chains"
  | "overview"
  | "fingerprints";

export interface Concept {
  id: string;
  kind: Kind;
  title: string;
  /** Who contributed it (module name). Used for grouping only; the front end **does not
      need to know what that module is**. */
  source: string;
  /** false = read-only (the contributor gave no Apply, e.g. a file with credentials). */
  writable: boolean;
  /** Non-empty = this card is **meaningless** right now, and the value is the reason (see
      lib/view's Concept.Locked): the UI greys out the whole card and disables the
      controls, but that sentence must be shown -- the user has to know why.
      The difference from read-only is *why* it cannot be touched: read-only means "there
      is no way to write back", locked means "the thing it manages does not exist right
      now"; the only use today is "this client is not installed on this machine". */
  locked?: string;
  /** true = this surface changes by itself, following that refresh every few seconds
      (declared by the contributor, see lib/view's Live). Not declared = not refreshed --
      whether "reading once is expensive" only the contributor knows; it cannot be guessed
      from Kind. */
  live?: boolean;
  /** Grouping in the navigation (see lib/view's Concept.Group): **two levels**, separated
      by "/" -- the first segment is the coarse tier (the backend's `Profiles`, already
      translated), the second the family (`claude`). Empty = belongs to no tier.
      Whether the heading is drawn is decided by TabStrip per "does this segment hold >= 2
      cards". **Affects layout only**, takes part in no identity judgment (routing and
      drafts both go by id). */
  group?: string;
  /** true = this card can take **the other half of the same file's draft** and ask "how
      should I render it" (see lib/view's Concept.Preview and api.ts's preview). That only
      one half can ask is the norm: the raw-text half is itself the raw text, there is no
      second half. */
  previewable?: boolean;
  /** Who comes first within the same section (see lib/view's Concept.Order, the backend
      serves it verbatim). The UI **does not interpret** these numbers, it only sorts by
      them -- the ordering rule is the contributor's product decision, not the UI's. */
  order?: number;
  /** Buttons on this card (see lib/view's Concept.Actions): **injected by the
      contributor**; the UI only draws them and forwards the click back. The reason they
      hang on this particular card is that the thing is about "**this one**" -- only it
      knows which "this one" the "make this one the default" refers to. */
  actions?: ConceptAction[];
  /** A one-line **status note** (see lib/view's Concept.Note), not a button: drawn as a
      static marker, clicking it does nothing. The only use today is "current config" on
      the tier card. It shares one set of tone words with Table cells (`ok`/`warn`/`bad`,
      anything else is treated as absent) -- it is a **statement**, so the UI must not
      draw it like a warning or an error. */
  note?: Note;
  data: any;
  /** Non-empty = this concept cannot be read right now (file deleted, JSON broken). The
      card still shows, with the reason written on it. */
  error?: string;
}

/** A one-line status note on a card (see lib/view's Note). `tone` empty = no color. */
export interface Note {
  text: string;
  tone?: "ok" | "warn" | "bad";
}

/**
 * One column of the sidebar: machine tag + human-readable name.
 *
 * The name is **reported by each module at registration time** (the backend's
 * `view.Title`); it is not hard-coded here, nor guessed from the source name -- if names
 * lived in the UI, adding a module would mean editing the UI once, and the rule "the
 * front end does not know modules" would be gone. The front end only draws it.
 *
 * It **does not go through t()**: that is the module's content (the same kind as concept
 * titles), and the backend has already translated it into the current language. t() is
 * for the UI's own words (buttons, hints).
 */
/** A button on a column (see lib/view's Section.Actions). The ID is for sending back,
    the label is the text. */
export interface SectionAction {
  id: string;
  label: string;
  /**
   * This action is **currently pressed** (`"bad"`); empty = the normal look. See the
   * kernel's view.Action.Tone.
   *
   * The UI uses it to draw the button as "a lit state" rather than "a clickable word" --
   * that is how the `auto fallback` one works: the name is fixed, the state is told by
   * color.
   */
  tone?: string;
}

export interface Section {
  source: string;
  /**
   * **What this column is for** (kernel view.SectionInfo.Fields). The UI uses it to
   * classify in the table of contents.
   *
   * The values line up with those kernel constants: `routes` (which path a request
   * takes), `config` (things that can be changed), `clients` (client settings); empty =
   * the column is not classified yet and is sorted last.
   */
  fields?: string;
  title: string;
  /** The actions on this column (like "create another profile file"). **Injected by the
      contributor** -- the thing just created has no concept yet, so it belongs to no card;
      the UI cannot know what that section can grow either. */
  actions?: SectionAction[];
  /** Grouping in the sidebar (see lib/view's Section.Group). Empty = belongs to no tier,
      sorted at the top. **Declared by the contributor**, so a single-member group still
      draws its heading -- deliberately different from TabStrip's ">= 2 draws": there the
      kernel derives the family from names (a heading over one member is noise), here the
      module itself says "which class I belong to", and what is said should be visible. */
  group?: string;
  /** "Fall back here when there is no position" -- the first screen lands on this column
      (see lib/view's Section.Default). **Declared by the contributor**: this column is a
      product choice ("let the user see one screen of everything first"), and the front
      end knows no module, so it has no way to judge who should be there. At most one
      column carries it (with several declarations the backend takes the smallest by
      Source), so looking for `true` here is enough, order does not matter. */
  default?: boolean;
}

export interface Snapshot {
  contract: number;
  generated_at: string;
  /** The language parsed out by the backend (see i18n.ts: the UI skeleton's text uses it
      to pick a catalog). */
  lang: string;
  /** The sidebar's column table: **every registered source**, including those that cannot
      produce a single concept right now. */
  sections: Section[];
  concepts: Concept[];
}

/** Conflict: both sides' original text is present, laid out for a human to decide. */
export interface Conflict {
  concept: string;
  path: string;
  base: string;
  current: string;
  yours: string;
  theirs: string;
}

export interface ApplyResult {
  ok?: boolean;
  base?: string;
  /** Which concept to switch to after it is done (currently only column actions give it,
      see view.Action.Run). The UI moves the route there -- **it does not know what that
      id is**, it just follows it. */
  focus?: string;
  conflict?: Conflict;
  error?: string;
}

/** Stays in sync with the backend Contract constant. The front end **must look at it
    first**; for an unknown version it would rather show a blank screen with one error. */
export const CONTRACT = 1;

/**
 * The runtime API address, derived from the **build-time base** (vite.config.ts's base is
 * "/ui/", so this is always "/ui/api").
 *
 * Why not the relative path `"api"` (which is what used to be here): resolving a relative
 * address depends on the **current document's** path, and that changes -- a user typing
 * `/ui` without the trailing slash is one. Measured: starting from `/ui`, `api/snapshot`
 * resolves to `/api/snapshot`; that path is not mounted, so it falls into the data plane's
 * catch-all and is treated as a malformed forward request (400, and it counts toward
 * data-plane traffic). The page itself renders (resource URLs are absolute), so the
 * symptom is "the UI looks fine, then every action errors" -- the most puzzling kind.
 *
 * An absolute address closes that road: the document's depth no longer affects the API
 * address. (module.go meanwhile redirects `/ui` -> `/ui/`, which is the shape a
 * human-facing URL should have; the two each solve half.)
 */
const API = `${import.meta.env.BASE_URL}api`;

/**
 * Turn the `error` coming back from the backend into a readable string.
 *
 * There it is **not guaranteed to be a string**: what the BFF itself returns is indeed a
 * string, but when the request does not land on the BFF (address computed wrong, prefix
 * changed, the port is not newgate at all), the other side may be an upstream error
 * forwarded up from the data plane -- an object shaped `{"type":"error","error":{…}}`.
 * `new Error(obj)` renders it as `[object Object]`, and a banner saying that leaves
 * whoever is debugging with no idea where to look.
 */
function errText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v == null) return "";
  if (typeof v === "object") {
    const o = v as { error?: unknown; message?: unknown };
    if (typeof o.message === "string") return o.message;
    if (o.error !== undefined && o.error !== v) {
      const inner = errText(o.error);
      if (inner) return inner;
    }
    try {
      return JSON.stringify(v);
    } catch {
      return String(v);
    }
  }
  return String(v);
}

async function json<T>(res: Response): Promise<T> {
  const text = await res.text();
  try {
    return JSON.parse(text) as T;
  } catch {
    // The upstream is not JSON (a reverse proxy stuck in an error page, or the port is not
    // newgate at all). Bring out the first few hundred bytes of the original text -- what
    // the other side is, only the person looking can tell.
    throw new Error(`${res.status} ${res.statusText}: ${text.slice(0, 300)}`);
  }
}

/**
 * Read a snapshot once. When `sources` is given, **only ask those contributors**.
 *
 * Auto-refresh goes this way: counters and log output are cheap, while the config one has
 * to re-read and re-parse every profile and every source file. No argument = everything
 * (which is what the first load wants).
 */
export async function snapshot(sources?: string[]): Promise<Snapshot> {
  const q = (sources ?? []).map((s) => `source=${encodeURIComponent(s)}`).join("&");
  const doc = await json<Snapshot & { error?: unknown }>(
    await fetch(`${API}/snapshot${q ? "?" + q : ""}`),
  );
  if (doc.error) throw new Error(errText(doc.error));
  if (doc.contract !== CONTRACT) {
    throw new Error(
      `the interface is version ${CONTRACT}, the backend speaks ${doc.contract} — reload after rebuilding`,
    );
  }
  // An old backend does not know sections (see the CONTRACT comment: this field was
  // **added**, the version number is not moved). Under such a backend the sidebar falls
  // back to source names -- ugly, but the page keeps working.
  doc.sections ??= [];
  return doc;
}

/** Save one concept's edit. base is the one received at load time (a content hash). */
export async function apply(id: string, base: string, edit: unknown): Promise<ApplyResult> {
  return json<ApplyResult>(
    await fetch(`${API}/apply`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, base, edit }),
    }),
  );
}

export interface PreviewResult {
  /** The data this concept should show when "the file looks like this" (same shape as
      data in a snapshot). */
  data?: unknown;
  /** The draft does not parse yet (**normal while typing**, not a failure): the UI keeps
      the previous look. */
  error?: string;
}

/**
 * One skin (see the backend's modules/web-dashboard/api.go Theme).
 *
 * All these fields are given by the **backend**: which skins are installed, what each is
 * called, whether it is a dark ground, and that string of token overrides. The front end
 * knows no skin -- it only injects `css` and turns `dark` into `color-scheme`. Adding a
 * skin = adding a module, this file does not change.
 */
export interface ThemeInfo {
  /** Machine tag (the one stored in state.json). Not translated. */
  id: string;
  /** Human-readable name (already translated by the backend, same path as concept
      titles). */
  name: string;
  /** Dark ground. The browser bits CSS variables do not cover (scrollbars, controls)
      follow it. */
  dark: boolean;
  /** Overrides for design tokens, shaped like `--bg: #000;`. **Tokens only, no
      selectors**. */
  css: string;
}

export interface Themes {
  /** The one in effect right now. **Empty = factory tokens** (not "could not read"). */
  active: string;
  themes: ThemeInfo[];
}

/** Read the skin table once. It is separate from the snapshot: a skin is the UI's own
    preference, not any module's contribution. */
export async function themes(): Promise<Themes> {
  return json<Themes>(await fetch(`${API}/themes`));
}

/** Switch to another skin. Empty id = back to the factory tokens. */
export async function setTheme(id: string): Promise<void> {
  const r = await json<{ ok?: boolean; error?: unknown }>(
    await fetch(`${API}/themes`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id }),
    }),
  );
  if (r.error) throw new Error(errText(r.error));
}

/**
 * Ask one question: "when this file's **not-yet-saved draft** looks like this, how should
 * this card render?"
 *
 * There is only one use, but it is critical: both halves of a file (controls + raw text)
 * can be edited. The user pastes a whole profile into the raw text, then goes to touch a
 * dropdown -- the controls half still holds the **pre-edit** on-disk content, so what it
 * submits is the whole old table and the paste is gone on the spot. With this question,
 * both halves always speak about the same content.
 *
 * Parsing is the **contributor's** job (that file's format is its knowledge), so here we
 * only move bytes.
 */
export async function preview(id: string, text: string): Promise<PreviewResult> {
  return json<PreviewResult>(
    await fetch(`${API}/preview`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, text }),
    }),
  );
}

/**
 * Run an action on a column, then the caller re-reads the snapshot (same as the save
 * path).
 *
 * It **takes no arguments**, deliberately: all the UI can offer is "the user clicked this
 * button"; everything else (what the new profile should be called, which fields to
 * prefill) must be decided by whoever owns that data, from what is on disk.
 *
 * The returned `focus` is **the id of the thing that action created** (only "create" has
 * one) -- the UI uses it to jump there instead of assembling an id itself and guessing.
 */
/** A button on one card (see lib/view's Concept.Actions). Same shape as SectionAction,
    only the target differs -- that one goes by source name, this one by concept ID. */
export interface ConceptAction {
  id: string;
  label: string;
  /** See SectionAction.tone. */
  tone?: string;
}

/**
 * Run an action hung on **one card** (like "make this one the default").
 *
 * It is separate from the section path because their identities differ (source name vs
 * concept ID). **The UI does not know what that action will do**: its label is a sentence
 * written by the contributor, and what it changes on disk only that one knows.
 */
export async function runConceptAction(id: string, action: string): Promise<ApplyResult> {
  return json<ApplyResult>(
    await fetch(`${API}/concept-action`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, action }),
    }),
  );
}

/** A button on one row (see lib/view's Row.Actions). Same shape as ConceptAction, with
    one more level for "which row": rows **grow out of data**, so the actions can only be
    determined when that row is constructed. */
export interface RowAction {
  id: string;
  label: string;
  /** See SectionAction.tone. */
  tone?: string;
}

/**
 * Run an action on **one row** of a table (like "probe this binding").
 *
 * Three identities are all required: which concept (that table), which row (the binding
 * key), which action. The UI **does not know** what the three together will do -- it only
 * knows which button was clicked.
 *
 * Like the other two action paths (section / concept) it **does not go through drafts and
 * does not touch CAS**: it is not "write this draft down", it is "do one thing" (fire a
 * liveness probe, then record the conclusion in the health table). That thing changes the
 * daemon's in-memory state, unrelated to the file this card reads, so this row should not
 * make the card dirty.
 */
export async function runRowAction(
  id: string,
  row: string,
  action: string,
): Promise<ApplyResult> {
  return json<ApplyResult>(
    await fetch(`${API}/row-action`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id, row, action }),
    }),
  );
}

export async function runSectionAction(source: string, action: string): Promise<ApplyResult> {
  return json<ApplyResult>(
    await fetch(`${API}/section`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source, action }),
    }),
  );
}

/**
 * A model an upstream can offer.
 *
 * `group` is the grouping **the upstream itself states** (some return `owned_by`, some
 * separate segments with `/` in the id). It only affects how the dropdown is arranged, it
 * takes part in no identity judgment -- a model's identity is always `id`.
 */
export interface ModelInfo {
  id: string;
  name?: string;
  group?: string;
}

/**
 * The answer to one "ask the upstream for its model list" (see the distribution's
 * modules/modellist).
 *
 * `supported` being false is a **normal answer**: most upstreams do not have this
 * endpoint, or this key is not allowed to list models. Then `models` is empty, `note` says
 * why, and the UI falls back to the list declared in providers.json -- rather than drawing
 * "could not ask" as a failure.
 */
export interface ModelList {
  provider: string;
  supported: boolean;
  /** The adapter that hit (`openai` / `anthropic`). Machine tag, not translated. */
  adapter?: string;
  /** The address actually asked (without credentials). */
  endpoint?: string;
  models: ModelInfo[];
  groups?: string[];
  fetched_at?: string;
  /** This copy came from the in-process cache (the UI says "cached" accordingly). */
  cached?: boolean;
  /** Explains the reason when `supported` is false (already translated by the backend). */
  note?: string;
}

/**
 * Ask one provider for its model list.
 *
 * It goes through the BFF's **extension endpoint** (`/api/ext/modellist`): the BFF does
 * not know modellist, it only forwards the request to the module registered under that
 * name. `refresh` being true skips the in-process cache.
 */
export async function modelList(provider: string, refresh = false): Promise<ModelList> {
  const doc = await json<ModelList & { error?: unknown }>(
    await fetch(`${API}/ext/modellist`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, refresh }),
    }),
  );
  if (doc.error) throw new Error(errText(doc.error));
  return doc;
}

/**
 * Fingerprint: the reference library says who this upstream actually is (see the
 * distribution's modules/verify).
 *
 * # Why the front end only recognizes one endpoint name
 *
 * Same as modellist: `/api/ext/fingerprint` is answered by **the module registered under
 * that name**, the BFF does not know it. The endpoint name is deliberately not the module
 * name -- what the front end needs to know is "there is a fingerprint endpoint", not
 * "there is a verify module". When the endpoint is missing, the dropdown and the panel
 * fall back to "no fingerprint yet" rather than erroring (which holds for old backends and
 * for assemblies without this module).
 */
export type FingerprintMatch =
  | "match"
  | "dated"
  | "family"
  | "mismatch"
  | "unknown"
  | "error";

export interface FingerprintCandidate {
  model: string;
  display_name?: string;
  family?: string;
  family_name?: string;
  score: number;
  probability?: number | null;
}

/**
 * One binding's fingerprint conclusion.
 *
 * `match` is a machine tag; the color and wording are the UI's own decision (see
 * `matchTone` / `matchText`): the backend does not know what words this screen uses, and
 * the front end must not translate it into a second identity.
 */
export interface FingerprintEntry {
  provider: string;
  model: string;
  api?: string;
  predicted?: string;
  predicted_name?: string;
  family?: string;
  family_name?: string;
  decision?: string;
  probability?: number | null;
  match: FingerprintMatch;
  used_outputs?: number;
  results?: FingerprintCandidate[];
  checked_at: string;
  build?: string;
  error?: string;
  cache_error?: string;
}

export interface FingerprintRole {
  role: string;
  provider?: string;
  model?: string;
  error?: string;
}

export interface FingerprintProfile {
  name: string;
  roles: FingerprintRole[];
}

export interface FingerprintProvider {
  name: string;
  protocol?: string;
  base_url?: string;
  has_key: boolean;
  models?: string[];
}

/** All the material for this screen (the backend's modules/verify State). */
export interface FingerprintState {
  profiles: FingerprintProfile[];
  providers: FingerprintProvider[];
  entries: FingerprintEntry[];
  build?: string;
  loaded: boolean;
  data_cached?: boolean;
  /** The last time the reference library cache was downloaded/checked (the cache file's
      mtime). */
  data_checked_at?: string;
  error?: string;
}

async function fingerprintExt<T>(body: unknown): Promise<T> {
  const res = await fetch(`${API}/ext/fingerprint`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const doc = await json<T & { error?: unknown }>(res);
  // Only **HTTP failure** is an error. State's own `error` is a statement that
  // "config/cache cannot be read", and the page still draws -- treating it as an
  // exception would blank the whole screen.
  if (!res.ok) {
    const msg = errText(doc.error);
    throw new Error(msg || `${res.status} ${res.statusText}`);
  }
  return doc as T;
}

/** This screen's material. Conclusions just written by verify in the terminal show up
    here too. */
export function fingerprintState(): Promise<FingerprintState> {
  return fingerprintExt<FingerprintState>({ action: "state" });
}

/** Look at the cache only (for the model dropdown): no provider = return everything. */
export async function fingerprintCache(
  provider = "",
  model = "",
): Promise<FingerprintEntry[]> {
  const doc = await fingerprintExt<{ entries: FingerprintEntry[] }>({
    action: "cache",
    provider,
    model,
  });
  return doc.entries ?? [];
}

/** Really run one round of fingerprinting. **Costs tokens**: the button's wording on the
    UI must say so. */
export function fingerprintRun(
  provider: string,
  model: string,
  api = "auto",
  count = 3,
): Promise<FingerprintEntry> {
  return fingerprintExt<FingerprintEntry>({
    action: "run",
    provider,
    model,
    api,
    count,
  });
}

// ---------- front-end plugins (contract: "front-end plugins" in api.go on the Go side) ----------
//
// The UI here **knows no plugin**: it only takes a manifest (name, entry address, which
// slots to fill), gives each plugin an empty container in its slot on its own layout, then
// hands the host over. What a plugin draws, which extension endpoint it asks, what it
// says -- all of that is the business of the module the plugin lives in.

export interface PluginDoc {
  name: string;
  /** Entry address relative to BASE_URL (with a content-hashed ?v=). */
  entry: string;
  slots: string[];
}

/** The host handed to plugins (version 1). */
export interface PluginHost {
  name: string;
  lang: string;
  ext<T = unknown>(name: string, body?: unknown): Promise<T>;
  asset(path: string): string;
}

let pluginList: Promise<PluginDoc[]> | null = null;

/** The plugin manifest as of opening this page. Asked once: plugins follow the binary, and
    changing the binary changes the page. */
export function pluginManifest(): Promise<PluginDoc[]> {
  pluginList ??= fetch(`${API}/plugins`)
    .then((r) => (r.ok ? r.json() : { plugins: [] }))
    .then((doc: { plugins?: PluginDoc[] }) => doc.plugins ?? [])
    // Old daemon without this route / network error: treat it as no plugins, the UI works
    // as usual.
    .catch(() => []);
  return pluginList;
}

export function pluginHost(name: string, lang: string): PluginHost {
  return {
    name,
    lang,
    async ext<T>(ext: string, body: unknown = {}): Promise<T> {
      const res = await fetch(`${API}/ext/${encodeURIComponent(ext)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const doc = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(errText((doc as { error?: unknown }).error) || `HTTP ${res.status}`);
      return doc as T;
    },
    asset(path: string): string {
      return `${import.meta.env.BASE_URL}plugins/${name}/${path}`;
    },
  };
}
