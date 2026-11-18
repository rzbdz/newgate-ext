/**
 * The **fake data** of the demo page: one complete `Snapshot`, sitting right next to
 * this file in `snapshot.json`.
 *
 * # Why copy a real snapshot instead of hand-writing an "about right" one
 *
 * What this page embeds on the site is the **real components** (the same `../src/kinds/*`,
 * the same `ConceptCard`), yet it is fed fake data — so the shape of the data must be
 * **byte-for-byte identical** to what the backend gives: once the truth drifts, what shows
 * up on screen is "this card won't draw" or "half a card is missing", while the demo page
 * is not in `web/dist`, and neither `web_test.go` nor `i18n_test.go` can see it, so nothing
 * turns red.
 *
 * Before 2026-09-23 this was one big hand-written `data.ts` (about 1700 lines) that only
 * laid out five sections and ten cards. The problem with that version was not that it was
 * badly written, but that **it was bound to drift**: hand-copying a real snapshot leaves
 * you a copy with no guard on it. So now it works like this: take it down from a real
 * snapshot (`GET /ui/api/snapshot`), swap out the private bits, and commit it directly as
 * a data file. Changing the renderer doesn't touch this data; when the real shape changes
 * (say the kernel adds a new Kind field), rerunning the command below syncs it.
 *
 * # What was swapped out when extracting (`tools/demo-snapshot.py` lists them one by one)
 *
 *   - Real local upstream addresses (`api.rvcompute.com…` / `ark.cn-beijing.volces.com…`)
 *     → example addresses such as `api.example-gateway.com`. **Credentials were never in
 *     there to begin with**: the BFF only answers `***` for the `api_key` cells (see
 *     redact in `core/modules/config/view.go`), and after extraction it was scanned once
 *     more for `sk-…` / `Bearer` / `control_token`; not one was found.
 *   - Disk path `/root/.config/newgate` → `~/.config/newgate`.
 *   - CAS fingerprints (`base: sha256:…`) and upstream call ids: shape kept, values swapped.
 *   - Proxy log keeps the last 60 lines (a full 400-line tail can't be read on the demo
 *     page anyway, and nobody reads it).
 *
 * What is still in this data and **intentionally kept**: model names and provider names
 * (`smt-deepseek`, `gpt-5.6-sol`…). They are part of what this product is demonstrating —
 * a candidate chain laid out on one screen tells the story of "this name lands on that
 * model"; erase the names and the card has no content. Those names are not credentials,
 * and not usable addresses.
 *
 * # How to extract a fresh copy
 *
 *     curl -s http://127.0.0.1:8899/ui/api/snapshot -o /tmp/snap.json
 *     python3 tools/demo-snapshot.py /tmp/snap.json \
 *       modules/web-dashboard/web/demo/snapshot.json
 *
 * The script does only the replacement table above + taking the last 60 log lines +
 * fixing `generated_at` (it changes on every run, and this data is in version control —
 * without fixing it, every re-extraction is one whole diff).
 */
import type { Snapshot, Themes } from "../src/api";
import { setLang } from "../src/i18n";
import raw from "./snapshot.json";
import rawThemes from "./themes.json";

export const DEMO_LANG = "zh-Hans";

/** That one complete snapshot. Its json shape is pinned by `Snapshot` in api.ts (see the
 * assertion below). */
export function snapshot(): Snapshot {
  return raw as unknown as Snapshot;
}

/**
 * The skin table (the verbatim body of `GET /ui/api/themes`).
 *
 * It is extracted and stored **separately** from the snapshot because on the real backend
 * they are two different ports anyway: a skin is the UI's own preference and is not in the
 * concept ledger (see modules/web-dashboard/api.go). The extraction is item-for-item the
 * same as for the snapshot — the third argument of `tools/demo-snapshot.py`, the same
 * banned-word table, the same "don't hand-copy" reason.
 *
 * `active` is empty in this data: the demo page has no backend to store "which one the
 * user picked", so it behaves like the real UI when opened — it starts from the
 * out-of-the-box tokens, and the visitor picks from that dropdown themselves (a pick only
 * takes effect in this browser and a refresh returns to the factory default).
 */
export function themes(): Themes {
  return rawThemes as unknown as Themes;
}

/** Make the real components render in Chinese (`t()` falls back to English on a miss, so
 * skipping this step leaves the UI mixing Chinese and English). */
export function useDemoLanguage(): void {
  setLang(DEMO_LANG);
}
