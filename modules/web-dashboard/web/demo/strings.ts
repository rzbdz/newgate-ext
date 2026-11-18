/**
 * The few sentences the demo page **owns**.
 *
 * # Why not put them in `src/i18n.ts`
 *
 * That dictionary is the **product's**, and a ratchet guards it:
 * `TestShippedBundleHasEverySentence` requires every sentence in the dictionary to appear
 * in the **shipped bundle** (`web/dist`). The demo page's output lands in `site/dist/demo`
 * and never enters `web/dist`, so adding a demo sentence to the product dictionary turns
 * that test red — and the error it reports ("this sentence is in the source dictionary
 * but not in the shipped bundle") would point the next person at "did you forget pnpm
 * build", which has nothing to do with the real cause (that is a demo-page sentence).
 *
 * So this side keeps its own, and the key is still **that English sentence** (the same msgid
 * tradition as the product); adding a language = adding a column.
 *
 * # Why only Chinese right now
 *
 * The site is Chinese-first today (see `site/src/site.json`: English is still unwritten).
 * The demo page lives inside the site, so its few sentences follow the site's language too.
 * The English column **stays here** — not forgotten, but writing it would be seen by nobody
 * (the site has no English page to jump over from).
 *
 * The rest of the words on the UI skeleton (save, undo, delete, catalog, not changed yet…)
 * **are not here**: they are product vocabulary and `t()` already has them. Writing them
 * twice only lets them drift into two different phrasings.
 */

// Only the sentences that appear **after something is clicked** (toasts) live here.
//
// There used to be one more: "this page is the real UI with fake data — none of your clicks
// leave the browser", a permanent line drawn in the page header. Removed: it framed this
// page as a "demo", whereas the point of this page is that **it is that UI**. A line that
// just sits there makes the reader see "this is fake" before they see the product itself;
// and anyone who really needs to know finds out by clicking anything — the four sentences
// left in this file are exactly what it said back then.
const zhHans: Record<string, string> = {
  "this is a demo — “{action}” would go to the real daemon; nothing is written here":
    "这是演示 —— “{action}”真的会去问守护进程，这里什么都没写",
  "this is a demo — “{action}” on “{row}” would talk to the real daemon":
    "这是演示 —— “{action}”（第 {row} 行）真的会去问守护进程",
  "this is a demo — save would write these files; here it writes nothing":
    "这是演示 —— 保存真的会把这几份文件写下去，这里什么都没写",
  "this is a demo — no file would be deleted": "这是演示 —— 不会真的删掉任何文件",
};

/** d looks up one sentence the demo page owns. Same semantics as the product's `t()`:
 * returns the English sentence as is on a miss. */
export function d(msg: string, args?: Record<string, string | number>): string {
  const s = zhHans[msg] ?? msg;
  if (!args) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in args ? String(args[k]) : m));
}
