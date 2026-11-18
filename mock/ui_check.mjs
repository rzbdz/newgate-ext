// The layer of checks that runs in the browser: **did the UI really render, does a click really hit the disk**.
//
// # Why a fifth layer is needed
//
// The Go layers (unit tests / the BFF e2e) verify the **contract**: what an endpoint
// returns, whether a status code is right. They cannot see things like "the UI put two
// checkboxes together", "two languages on one screen", "clicking save does nothing" -- on 2026-09-20 we actually rendered it once and caught three at once, while **no test went red**.
//
// # Why it is not in CI
//
// It needs a browser. CI does not use node at all (the frontend build is checked in,
// Go embeds it directly), and installing a chromium just for this layer buys little.
// So, like the e2e that needs a real token: **run locally by the developer**, see mock/ui_check.sh.
//
// How to run it is ui_check.sh's job (start the sandbox, start the daemon, call this
// script); this file takes two arguments: the UI address (with trailing slash), and
// the sandbox's state.json (the conflict section edits it **from outside**, playing the CLI that writes first). A failed assertion exits non-zero, and the reason goes on screen.
//
// # How to find things
//
// Always find things by **machine markers**, never by translated text: a sidebar row has `title="<source>"`, a tab has
// `title="<concept id>"`. Finding by text would make this script follow the language (it runs under zh-Hans),
// and changing one translation would turn it red -- a red with no information in it.

import fs from "node:fs";
import path from "node:path";

const url = process.argv[2];
const stateFile = process.argv[3];
if (!url || !stateFile) {
  console.error("usage: node ui_check.mjs <ui address> <sandbox state.json path>");
  process.exit(2);
}
// The variant without a trailing slash: it is the form **people type**, and the scene of
// the real bug on 2026-09-20 (relative addresses resolve against it, so API requests fall out of the /ui mount point).
const bare = url.replace(/\/+$/, "");

// Dependency resolution: look for playwright in node_modules by default; on this machine
// it may live elsewhere -- point at it with PLAYWRIGHT_MODULE (same rule as elsewhere: machine-specific paths go through env vars).
async function loadPlaywright() {
  const tries = [process.env.PLAYWRIGHT_MODULE, "playwright"].filter(Boolean);
  for (const spec of tries) {
    try {
      return await import(spec);
    } catch {
      /* try the next one */
    }
  }
  console.error(
    "playwright not found. Install one (it is **not** a dependency of this repo, see the file header for why):\n" +
      "  cd modules/web-dashboard/web && pnpm add -D playwright\n" +
      "if it is installed elsewhere, point at it with an env var: PLAYWRIGHT_MODULE=/path/playwright/index.mjs",
  );
  process.exit(2);
}

const { chromium } = await loadPlaywright();

let pass = 0;
let fail = 0;
const ok = (m) => { console.log(`  ✓ ${m}`); pass++; };
const bad = (m) => { console.log(`  ✗ ${m}`); fail++; };
const check = (m, cond, detail = "") => (cond ? ok(m) : bad(`${m}${detail ? " — " + detail : ""}`));
const skip = (m) => console.log(`  · ${m}`);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });

// No error thrown by the page itself is allowed: a white screen, a crashed component, a blowup in an effect -- all surface here first.
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));
page.on("console", (m) => {
  if (m.type() !== "error") return;
  const text = m.text();
  // The 409 is the **expected** result of the conflict section: the browser logs a console
  // error for any non-2xx fetch, and "stale baseline" is exactly the situation created
  // there. It is not a page error -- counting it would make this assertion "do not test conflicts". The exclusion is narrow; other errors still count.
  if (text.includes("status of 409")) return;
  // Check 21 **deliberately** makes an inline action fail (the sandbox provider points
  // at a dead port); the BFF returns 400 for "the action did not go through", so here too there is a console error. Same again: not a page error.
  //
  // The exclusion is pinned to the **endpoint** (`location().url`), not just the status: any
  // 400 from anywhere else on `/api/*` is still a real signal and must not be hit by this exclusion.
  const where = m.location?.()?.url ?? "";
  if (text.includes("status of 400") && where.endsWith("/api/row-action")) return;
  pageErrors.push("console: " + text);
});

// With unsaved changes the UI installs a beforeunload prompt (see App.svelte). Playwright
// **auto-dismisses** all dialogs by default, and dismissing beforeunload means "stay
// here" -- the reload below would then silently not happen and every later assertion
// would be testing a page that was never refreshed. So accept it explicitly here.
//
// confirm is accepted too: an irreversible action like deleting a profile file **should**
// ask first (see the delete button in MappingEditor), and asking plus recording the
// question lets us assert below that "it did ask". Anything else counts as a page error -- an unexpected modal is a bug.
const confirms = [];
page.on("dialog", (d) => {
  if (d.type() === "beforeunload") return void d.accept();
  if (d.type() === "confirm") {
    confirms.push(d.message());
    return void d.accept();
  }
  pageErrors.push(`dialog(${d.type()}): ${d.message()}`);
  void d.dismiss();
});

// The backend snapshot: section titles and concept ids are taken from here (**never guess translations**).
async function fetchSnapshot() {
  const r = await fetch(bare + "/api/snapshot");
  return r.json();
}

// -- 0. Typing `/ui` without a trailing slash must still work --
//
// This is the regression for a real bug: porthub accepts the bare prefix, so `/ui` gets
// in and the page renders (asset URLs are absolute), but the **runtime API URL** is
// resolved from the document path -- starting from `/ui`, `api/snapshot` becomes
// `/api/snapshot`, falls out of the mount point into the data plane, and is treated as a forward of an unrecognized shape (400). The symptom is "the UI looks fine, every action errors" -- hard to connect to the URL.
{
  const p = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const errs = [];
  p.on("pageerror", (e) => errs.push(String(e)));
  await p.goto(bare, { waitUntil: "networkidle" });
  await p.waitForTimeout(800);
  const n = await p.locator("section.card").count();
  check("`/ui` (without trailing slash) also works", n > 0, `${n} cards; address ${p.url()}`);
  check("`/ui` requests land back under /ui/", new URL(p.url()).pathname.startsWith("/ui/"), p.url());
  const banner = await p.locator(".banner").count();
  check("`/ui` shows no error banner", banner === 0, await p.locator(".banner").first().innerText().catch(() => ""));
  check("`/ui` has no page errors", errs.length === 0, errs.slice(0, 2).join(" / "));
  await p.close();
}

await page.goto(url, { waitUntil: "networkidle" });
await page.waitForTimeout(1200);

const snap = await fetchSnapshot();
const titleOf = (source) => (snap.sections.find((s) => s.source === source) || {}).title || "";

// -- 1. It rendered --
const cards = await page.locator("section.card").count();
check("cards rendered", cards > 0, `${cards} actual`);
check("the page threw nothing", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

// -- 2. No longer "one waterfall" --
//
// Before the redesign: all cards were laid out downward grouped by source, measured at
// 4303px ≈ 4.8 screens; a four-step job like "flip one switch" started with three screens
// of scrolling. Two criteria: group headings are gone, and full page height is about one screen (leaving some slack for lots of content on wide screens).
{
  const headers = await page.locator("h2.srch").count();
  check("group headings (fossils of the waterfall) are gone", headers === 0, `${headers} left`);
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  console.log(`  · full page height ${h}px (before the redesign: 4303px ≈ 4.8 screens)`);
  check("full page height is back within one screen", h < 900 * 1.5, `actual ${h}px`);
  const side = await page.locator("nav.side button").count();
  check("the sidebar lists the sections", side > 0, `actual ${side} rows`);
}

// -- 3. One language per screen --
//
// This is the ratchet for the 2026-09-20 bug: the language t() reads lived in a module-
// level variable, not reactive state, so strings with no reactive dependency froze at
// the language of **first render** -- under zh-Hans, "44 concepts" was Chinese while "save" was English in the same row.
//
// The criterion uses **a few words from the UI skeleton** (button, input placeholder):
// they are the ones with "no reactive dependency", exactly the ones that froze. When the language is not English, they should no longer be the English original.
const lang = snap.lang;
const saveText = (await page.locator("header.top button.primary").innerText()).trim();
const placeholder = await page.locator("header.top input.filter").getAttribute("placeholder");
if (lang === "en" || !lang) {
  skip(`language is ${lang}, skipping the 'one language only' check`);
} else {
  // The key is the English sentence itself, so "still the English original" = not translated.
  check("the button follows the UI language", saveText !== "save", `save button shows ${JSON.stringify(saveText)}`);
  check("the input placeholder follows the UI language", !placeholder.startsWith("filter"), `actual ${JSON.stringify(placeholder)}`);
}

// -- 4. Section titles come from the backend (reported by the module), not the source name --
//
// Dropping it would not make anything error: the frontend falls back to the source name,
// so the sidebar shows machine markers like config / plugin-manager, looking merely like
// "not translated yet". So verify here that it matches the snapshot's title and is not equal to the source.
if (snap.sections.length) {
  const first = snap.sections[0];
  const shown = (await page.locator(`nav.side button[title="${first.source}"]`).first().innerText()).trim();
  check("the sidebar uses the section title from the backend", shown.startsWith(first.title), `on screen: ${JSON.stringify(shown)}, in the snapshot: ${JSON.stringify(first.title)}`);
  if (lang !== "en" && lang) {
    check("the section title is translated (not the source name)", shown !== first.source, `still shows ${JSON.stringify(shown)}`);
  }
}

// -- 5. Split panes: the two halves of one file side by side --
//
// This is the most common "change config" action (what this value looks like in the file); before the redesign it meant scrolling over and back.
{
  // Find a pair by **data** (same convention as fileOf in nav.ts: the controls half carries
  // file, the raw half carries path, and the same relative path means the same file). Do
  // not hardcode config -- that is a module name; accepting it in the test would leave the "does not know modules" rule in the UI only half enforced.
  const fileOf = (c) => c.data?.file ?? c.data?.path;
  const pair = snap.concepts.find(
    (c) =>
      c.kind !== "code" &&
      fileOf(c) &&
      snap.concepts.some((x) => x.kind === "code" && x.source === c.source && fileOf(x) === fileOf(c)),
  );
  if (!pair) {
    skip("this assembly has no 'two halves of one file', skipping the side-by-side check");
  } else {
    await page.locator(`nav.side button[title="${pair.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`button.tab[title="${pair.id}"], nav.v button[title="${pair.id}"]`).first().click().catch(() => {});
    await page.waitForTimeout(300);
    const split = await page.locator(".split.two").count();
    const panes = await page.locator(".pane-l, .pane-r").count();
    check("controls and raw text are side by side", split > 0 && panes === 2, `actual ${panes} panes`);
    const right = await page.locator(".pane-r .mono").first().innerText().catch(() => "");
    check("the right pane is that file's raw text", right.includes(fileOf(pair)), `right pane: ${JSON.stringify(right)}`);
  }
}

// -- 6. With many cards, horizontal tabs give way to a left vertical column --
//
// This is the regression for "config's 38 horizontal tabs would not scroll": sections above
// the threshold (TAB_OVERFLOW=8 in App) switch the tab strip to a vertical scrolling list
// on the left of the content. The threshold is content-driven (not width), so which
// assertion runs is decided by "the number of config cards" -- the sandbox injects 9 filler profiles, so normally the vertical-column half runs.
{
  // Navigation counts **files**, not cards: when one file has "controls + raw" halves, only
  // the controls half is listed; the raw text is just the right pane (see navUnits in
  // App.svelte). So the expected value here cannot just count concepts; fold by the same
  // rule first -- otherwise this assertion reports the **intentional change** of "raw text no longer takes a slot" as a failure.
  const fileOf = (c) => c.data?.file ?? c.data?.path;
  const navUnits = (list) => {
    const byFile = new Map();
    for (const c of list) {
      const f = fileOf(c);
      if (!f) continue;
      const k = c.source + "|" + f;
      byFile.set(k, [...(byFile.get(k) ?? []), c]);
    }
    return list.filter((c) => {
      const f = fileOf(c);
      if (!f) return true;
      const control = byFile.get(c.source + "|" + f).find((g) => g.kind !== "code");
      return !(control && c.kind === "code");
    });
  };
  const cfgCards = navUnits(snap.concepts.filter((c) => c.source === "config"));
  const n = cfgCards.length;
  await page.locator(`nav.side button[title="config"]`).click();
  await page.waitForTimeout(400);
  const rows = await page.locator(".content nav.v").count();
  const horiz = await page.locator(".content .tabs").count();
  if (n > 8) {
    check("many cards switch to a left vertical column", rows === 1, `nav.v appears ${rows} times`);
    check("no horizontal tab strip when the vertical column is shown", horiz === 0, `${horiz} horizontal ones left`);
    const items = await page.locator(".content nav.v button").count();
    check("the vertical column lists every card in this section", items === n, `${items} vs ${n}`);
    // The vertical column must be a scroll container. The regression point is not "did it
    // actually overflow this time" (whether the sandbox has enough cards to fill a screen is
    // a matter of content), but "is the container set to scroll" -- the user's bug was 38 rows laid out that would never scroll because the inner layer's height followed the content. The mechanism has to be there for overflow to take effect.
    const slotCss = await page.evaluate(() => {
      const slot = document.querySelector(".content.subcol > .nav-slot");
      return slot ? getComputedStyle(slot).overflowY : "";
    });
    check("the vertical column is a scroll container (the wheel works)", slotCss === "auto", `overflow-y=${slotCss}`);
    // Clicking a card in the vertical column must really switch (pick from the ones that **appear in the vertical column** -- the raw half is not there)
    const target = cfgCards[1];
    await page.locator(`.content nav.v button[title="${target.id}"]`).click();
    await page.waitForTimeout(300);
    const on = await page.locator(".content nav.v button.on").getAttribute("title");
    check("clicking a card in the vertical column switches to it", on === target.id, `actually selected ${on}`);

    // -- Grouping: top-level kind (`tier`) and family (`claude`) --
    //
    // The user wanted "the two things you configure right after install" and "the pile that
    // grows day by day" separated on the page (original words: "profile here could just get a
    // column split"). Titles come from the backend's two-level group (`tier/claude`); whether one is drawn is decided here by "are there ≥2 cards under this run".
    //
    // The criterion cannot just be "a heading exists": if the cards below a small caption are
    // not indented, the caption is just a comment and the reader still cannot tell those two
    // belong to it. So measure **indentation** here -- every card after a heading must sit further right than the heading itself.
    const groups = await page.locator(".content nav.v .group").count();
    check("the vertical column has group headings (top-level kind / profile family)", groups > 0, `actual ${groups}`);
    const nesting = await page.evaluate(() => {
      const nav = document.querySelector(".content nav.v");
      const kids = [...nav.children];
      const at = kids.findIndex((e) => e.classList.contains("group"));
      if (at < 0) return null;
      const head = parseFloat(getComputedStyle(kids[at]).paddingLeft);
      const after = kids.slice(at + 1).filter((e) => e.classList.contains("row"));
      return {
        head,
        n: after.length,
        flat: after
          .filter((e) => parseFloat(getComputedStyle(e).paddingLeft) <= head)
          .map((e) => e.textContent.trim().slice(0, 24)),
      };
    });
    check(
      "cards under a heading are really indented (not just a caption added)",
      !!nesting && nesting.n > 0 && nesting.flat.length === 0,
      JSON.stringify(nesting),
    );
  } else {
    skip(`config cards in this assembly do not exceed the threshold (${n} ≤ 8), the vertical-column checks do not apply`);
  }
}

// -- 7. A click really hits the disk --
//
// Only test the **writable toggles** (boolean switches): it is one step in the "fewest
// clicks" design, and the path by which a user changes runtime behavior in the UI.
//
// After the redesign it sits inside a section, behind a tab -- so walk there first. **This
// step is itself the acceptance for the layout redesign**: sidebar once, tab once, switch once, save once = four operations (before the redesign it was three screens of scrolling).
const box = page.locator('section.card input[type="checkbox"]').first();
async function openSwitches() {
  const src = snap.sections.find((s) => s.source === "plugin-manager");
  const sw = snap.concepts.find((c) => c.id === "plugin-manager.switches");
  if (!src || !sw) return false;
  await page.locator(`nav.side button[title="${src.source}"]`).click();
  await page.waitForTimeout(200);
  await page.locator(`button.tab[title="${sw.id}"], nav.v button[title="${sw.id}"]`).first().click();
  await page.waitForTimeout(250);
  return true;
}
if (!(await openSwitches())) {
  skip("this assembly has no plugin-manager switches card, skipping the save and conflict sections");
} else {
  await box.click();
  await page.waitForTimeout(200);
  const before = (await page.locator("header.top button.primary").innerText()).trim();
  check("one change shows 'unsaved'", /\d/.test(before), `save button shows ${JSON.stringify(before)}`);

  // Switch to another section and back: the draft **must not be lost** (the layout changed; this semantics did not).
  const conf = snap.sections.find((s) => s.source !== "plugin-manager");
  if (conf) {
    await page.locator(`nav.side button[title="${conf.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`nav.side button[title="plugin-manager"]`).click();
    await page.waitForTimeout(200);
    const still = (await page.locator("header.top button.primary").innerText()).trim();
    check("the draft survives switching sections and back", /\d/.test(still), `save button shows ${JSON.stringify(still)}`);

    // Also: the selected section goes into the URL and survives a reload (part of "fewer operations").
    //
    // Note the draft **does not survive** a reload: it lives in page memory, and a reload
    // is a fresh start (the beforeunload in App blocks just before the real reload, see
    // the comment there). So after reloading we click the switch again to create a draft -- not a workaround, that is what this semantics is.
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForTimeout(900);
    const active = await page.locator("nav.side button.on").getAttribute("title");
    check("still on the same section after reload", active === "plugin-manager", `actual ${active}`);
    await openSwitches();
    await box.click();
    await page.waitForTimeout(200);
  }

  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(900);
  const after = (await page.locator("header.top button.primary").innerText()).trim();
  check("after saving it is back to 'nothing unsaved'", !/\d/.test(after), `save button shows ${JSON.stringify(after)}`);

  // -- 8. Someone else wrote first -> show a conflict, and **never overwrite** --
  //
  // This is the only place in the product where two writers change the same file (the CLI
  // and the browser), and the semantics explicitly asked for: "on commit, detect whether there is a conflict, and if so, prompt the user".
  //
  // One thing only this browser layer can check: **the dialog really appears in front of
  // the user**. The Go e2e verifies the 409 and those fields; it cannot see whether the screen shows a card with both sides' raw text or just a blank.
  const raw = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  raw.written_by_someone_else = true;
  fs.writeFileSync(stateFile, JSON.stringify(raw, null, 2));

  await box.click();
  await page.waitForTimeout(150);
  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(900);

  const dialog = page.locator(".banner.conflict");
  const shown = await dialog.count();
  check("a conflict appears after someone else wrote first", shown > 0, "nothing appeared on screen; the user's changes just vanished");
  if (shown) {
    // Both sides' raw text must be shown, or the user cannot decide whose to keep.
    const text = await dialog.first().innerText();
    check("the conflict names the file", text.includes(stateFile), text.slice(0, 80));
    check("the conflict offers two choices", /keep mine/i.test(text) || text.includes("保留"), text.slice(0, 80));
  }
  const after2 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  check("the rejected write did not overwrite the disk", after2.written_by_someone_else === true);
}

// -- 9. provider table: credentials never leave, yet it can be added to and edited --
//
// This section is the acceptance for "the user wants to configure providers in the web
// UI". It verifies something that looks contradictory: **the browser never gets the key,
// yet can still edit the provider**. The trick is the value never enters the snapshot (FieldSecret); an empty string sent back from the UI = leave it alone. So verify both:
//
//   - the field arrives empty with type password (credentials do not leave);
//   - after changing something else and saving, the key on disk is **still there** (empty string was not taken as "delete").
//
// If the second one is wrong, the symptom is "change base_url once, the next request is 401", with the save reporting success.
{
  const providersFile = stateFile.replace(/state\.json$/, "providers.json");
  const before = fs.existsSync(providersFile) ? fs.readFileSync(providersFile, "utf8") : "";
  const keyBefore = (JSON.parse(before || "{}").providers?.demo?.api_key) ?? "";

  const card = snap.concepts.find((c) => c.kind === "records" && c.data?.file === "providers.json");
  if (!card) {
    skip("this assembly has no provider table card, skipping the provider checks");
  } else {
    await page.locator(`nav.side button[title="${card.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`button.tab[title="${card.id}"], nav.v button[title="${card.id}"]`).first().click();
    await page.waitForTimeout(300);

    const keyBox = page.locator('section.card input[type="password"]').first();
    check("the credential field is masked", (await keyBox.count()) > 0);
    check("the credential value did not come with the snapshot", (await keyBox.inputValue()) === "", "the browser should not have this value");

    // Change a field that is **not a credential**, save, then look at the disk.
    //
    // The conflict the previous section deliberately created is still on screen (that is
    // what it verifies), so "no error" means **no new banner**, not "no banner on screen at all".
    const bannerBefore = await page.locator(".banner").first().innerText().catch(() => "");
    // The field id is `f-<this record's id>-<field name>` (the record id is now the provider
    // name, no longer the array index -- see the comment separating key/id in
    // kinds/Records.svelte). So find by **suffix**; do not hardcode the id.
    const baseURL = page.locator(`input[id$="-base_url"]`).first();
    await baseURL.fill("http://127.0.0.1:9/changed");
    await page.waitForTimeout(150);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(900);
    // A save error must be spoken. Without this check, "it was not written" and "it was
    // written but I read it wrong" look identical on screen -- the real reason the file did not change gets guessed a dozen ways.
    const banner = await page.locator(".banner").first().innerText().catch(() => "");
    check("saving added no new error", banner === bannerBefore, `${banner.slice(0, 120)}`.trim());
    const after = JSON.parse(fs.readFileSync(providersFile, "utf8")).providers?.demo ?? {};
    check("provider fields can be edited", after.base_url === "http://127.0.0.1:9/changed", JSON.stringify(after));
    check("sending back an empty credential field did not lose the key", after.api_key === keyBefore && keyBefore !== "", `now ${JSON.stringify(after.api_key)}`);

    // The eye: clicking turns the input into plain text. The value is one we just typed,
    // so this verifies "that button really is wired to this field" -- pasting a key and glancing at it is the most common action.
    //
    // Find the field by **id** (suffix match, reason as above), not by `input[type=password]`:
    // after revealing, the type changes, so using the type as a selector becomes "element
    // does not exist" and times out -- which looks like the UI is broken but is really testing something that no longer exists.
    const keyField = page.locator(`input[id$="-api_key"]`).first();
    await keyField.fill("sk-typed-in-the-browser");
    await page.waitForTimeout(150);
    await page.locator(".rec button[title]").first().click();
    await page.waitForTimeout(150);
    check("the eye turns that field into plain text", (await keyField.getAttribute("type")) === "text");
  }
}

// -- 10. All actions for a profile file live on its own kv card --
//
// Before 2026-09-20: create/rename/delete lived on a "profile files" directory card, while
// profiles lived on each profile card -- one file spoken of by two cards, and the files
// listed by that directory card corresponded one-to-one with these cards (original words: "this one is redundant... I want it removed entirely"). Now the actions all live on the card itself:
// `+ profile` creates a new one, the name field renames, the inherit field picks the parent
// profile, `delete this file` deletes the whole file (asking first).
{
  const home = stateFile.slice(0, stateFile.lastIndexOf("/"));
  const mappingsDir = home + "/mappings";
  const has = (n) => fs.existsSync(`${mappingsDir}/${n}.kv`);

  // That redundant directory card must **really be gone** (it was explicitly asked to be removed; if it comes back someone should shout).
  check(
    "the redundant 'profile files' directory card is gone",
    !snap.concepts.some((c) => c.id === "config.profiles"),
    snap.concepts.filter((c) => c.id.startsWith("config.profile")).map((c) => c.id).join(" "),
  );

  // Start from a blank slate (earlier sections deliberately created conflicts; drafts would disturb this one).
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(700);

  const anyProfile = snap.concepts.find((c) => c.kind === "mapping-editor");
  await page.locator(`nav.side button[title="${anyProfile.source}"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="${anyProfile.id}"], nav.v button[title="${anyProfile.id}"]`)
    .first()
    .click();
  await page.waitForTimeout(400);

  // -- Create: a button on **this section's toolbar** --
  //
  // It is not on any card (moved from the card head on 2026-09-21): the newly created one has
  // no concept yet, so no card can host it. On the section it is always within reach, whichever card you are looking at.
  await page.locator(".secbar button").first().click();
  // Both things must be awaited, and it is **write to disk first, then switch cards** (the switch happens in the load after apply).
  // Waiting only for the file would assert the URL before navigation; that red would be the test's timing, not the product.
  for (let i = 0; i < 40 && !has("new-profile"); i++) await page.waitForTimeout(100);
  check("`+ profile` created a new profile file", has("new-profile"), `mappings contains ${fs.readdirSync(mappingsDir).join(" ")}`);
  for (let i = 0; i < 40 && !page.url().includes("config.profile.new-profile"); i++) {
    await page.waitForTimeout(100);
  }
  check("right after creating, it switches to that card", page.url().includes("config.profile.new-profile"), page.url());

  // -- Rename + inherit: both fields are in the card head; save once and both land --
  await page.locator(".card .head input.name").fill("uicheck-renamed");
  await page.locator(".card .head select").selectOption("demo");
  await page.waitForTimeout(200);
  await page.locator("header.top button.primary").click();
  for (let i = 0; i < 40 && !has("uicheck-renamed"); i++) await page.waitForTimeout(100);
  check("renaming moved the file to the new name", has("uicheck-renamed") && !has("new-profile"),
    `new-profile=${has("new-profile")} uicheck-renamed=${has("uicheck-renamed")}`);
  const renamed = has("uicheck-renamed") ? fs.readFileSync(`${mappingsDir}/uicheck-renamed.kv`, "utf8") : "";
  check("the inheritance was written into the file", renamed.includes("extends=demo"), JSON.stringify(renamed.slice(0, 80)));

  // -- Delete: ask once, then really remove from disk --
  const before = confirms.length;
  await page
    .locator(`button.tab[title="config.profile.uicheck-renamed"], nav.v button[title="config.profile.uicheck-renamed"]`)
    .first()
    .click();
  await page.waitForTimeout(350);
  await page.locator(".card .head button.danger").click();
  for (let i = 0; i < 30 && has("uicheck-renamed"); i++) await page.waitForTimeout(100);
  check("it asked before deleting", confirms.length > before, `confirms=${JSON.stringify(confirms.slice(before))}`);
  check("deleting really removed the file", !has("uicheck-renamed"));
}


// -- 11. Typing in the text editor does not jump back to the first line (regression seen live on 2026-09-20) --
//
// The scene: press `d` once in the raw pane and the cursor jumps to the first line; editing
// is impossible. The root cause was that CodeMirror's `$effect` read `value` (reactive), so
// **every character typed** rebuilt the editor, poured the doc in from the top and reset the cursor to 0 (see untrack in kinds/CodeEditor.svelte).
//
// The criterion is "the typed characters are at the end", not "where the cursor is": the symptom
// of jumping back to the start is characters being **inserted in reverse** at the front, and this check can tell.
{
  // Pick a **writable** raw pane: a profile's right pane is `.kv` (no credentials, not
  // read-only), while the right pane of something like providers.json with an api_key is
  // **read-only** (redacted) -- typing into a read-only editor does nothing, and that "failure" tests something else.
  const editable = snap.concepts.find((c) => c.kind === "mapping-editor");
  await page.locator(`nav.side button[title="${editable.source}"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="${editable.id}"], nav.v button[title="${editable.id}"]`)
    .first()
    .click();
  await page.waitForTimeout(400);

  const cm = page.locator(".pane-r .cm-content").first();
  if (await cm.count()) {
    await cm.click();
    await page.keyboard.press("Control+End");
    await page.keyboard.type("xyz");
    await page.waitForTimeout(300);
    const text = (await cm.innerText()).trimEnd();
    check("typing three characters in the text editor does not jump back to the start", text.endsWith("xyz"),
      `ends with ${JSON.stringify(text.slice(-24))}`);
    // Leaving a dirty draft would disturb the keyboard checks later; revert it.
    const revert = page.locator(".card.dirty button").filter({ hasText: /撤销|revert|Revert/ }).first();
    if (await revert.count()) { await revert.click(); await page.waitForTimeout(300); }
  } else {
    skip("the text editor did not render (did the template change?) -- skipping this check");
  }
}

// -- 11.5. Displacing the other half's draft must be announced --
//
// Both halves write the same file, so the half edited later wins and the other half's
// **unsaved** draft is discarded (see edit/lastEdit in App.svelte; that is the semantics
// the user asked for). But what is discarded is text they just typed -- dropping it silently breaks this repo's hard rule (never silent), and they would think the text is still there.
//
// Two criteria: a notice appears on screen, and it **names which file** (only two halves of
// the same file can collide like this; not naming the file makes the user guess which one).
{
  const pair = snap.concepts.find((c) => c.kind === "mapping-editor");
  await page.locator(`nav.side button[title="${pair.source}"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="${pair.id}"], nav.v button[title="${pair.id}"]`)
    .first()
    .click();
  await page.waitForTimeout(400);

  const cm = page.locator(".pane-r .cm-content").first();
  const name = page.locator(".card .head input.name").first();
  if ((await cm.count()) && (await name.count())) {
    // First type something in the **raw** pane (create a raw draft), then touch the **controls** pane.
    await cm.click();
    await page.keyboard.press("Control+End");
    await page.keyboard.type("# raw half");
    await page.waitForTimeout(250);
    await name.fill("demo-renamed");
    await page.waitForTimeout(300);
    const notice = await page.locator(".notice").first().innerText().catch(() => "");
    check("displacing the other half's draft is announced", notice.trim().length > 0, "not a word on screen; that text is just gone");
    check(
      "the notice names which file",
      notice.includes(pair.data.file),
      `actual ${JSON.stringify(notice.slice(0, 90))}`,
    );
    // Wrap up: revert this section's draft so it does not disturb the keyboard checks later.
    await page
      .locator(".card.dirty button")
      .filter({ hasText: /撤销|revert|Revert/ })
      .first()
      .click()
      .catch(() => {});
    await page.waitForTimeout(400);
  } else {
    skip("this section has no 'controls + raw' halves, skipping 11.5");
  }
}

// -- 11.6. Edit the raw text and the controls half follows --
//
// This is the one the user explicitly asked for: "refresh the ui controls right after
// editing the raw". Without it, the controls half still holds the on-disk content from
// **before the change**, and its edit payload is the **whole file** -- touching one
// dropdown then submits the entire old table, and what was just pasted into the raw text is gone on the spot, never having been shown on screen (see Concept.Preview in the kernel's lib/view).
//
// The criterion lands on **the controls column really gaining a role**, not "the backend
// returned 200": a 200 with an unchanged UI is nowhere near what this check wants.
//
// Use a `.kv` profile (fill1..fill9 laid down by the sandbox script): appending a line
// `key=provider/model` at the end is valid KV, whereas the JSON one must be inserted inside the braces, which is hard to construct cleanly by typing.
{
  const kvProfile = snap.concepts.find((c) => c.kind === "mapping-editor" && c.data.file?.endsWith(".kv"));
  if (!kvProfile) {
    skip("this assembly has no .kv profile, skipping 11.6");
  } else {
    await page.locator(`nav.side button[title="${kvProfile.source}"]`).click();
    await page.waitForTimeout(250);
    await page
      .locator(`button.tab[title="${kvProfile.id}"], nav.v button[title="${kvProfile.id}"]`)
      .first()
      .click();
    await page.waitForTimeout(400);

    const before = await page.locator(".pane-l .role b.mono").allInnerTexts();
    const cm = page.locator(".pane-r .cm-content").first();
    await cm.click();
    await page.keyboard.press("Control+End");
    // `role.<name>=` is how KV writes "any role name" (`zzprobe=` is rejected by the parser --
    // it prefers an error on a key it does not know, because a mistyped key used to be silently ignored).
    await page.keyboard.type("\nrole.zzprobe=demo/demo-model");
    // 200ms debounce + one round trip, with plenty of slack (this is the local BFF; normally a few milliseconds).
    await page.waitForTimeout(1200);
    const after = await page.locator(".pane-l .role b.mono").allInnerTexts();
    check(
      "the role added in the raw text shows up in the controls half",
      after.includes("zzprobe") && !before.includes("zzprobe"),
      `before [${before}] after [${after}]`,
    );

    // -- Then change one field in the controls and save: **both edits must be on disk** --
    //
    // This check is the whole reason the feature exists (original words: "refresh the ui
    // controls right after editing the raw"). The controls half's edit payload is the **whole
    // file**, so what it submits must be built on the raw draft; otherwise "what was added in the raw text" and "the field changed in the controls" cannot both survive.
    //
    // The bad behavior seen live (before the fix): the save wrote **the raw half, content =
    // exactly what was on disk** -- both edits vanished together, with not a single error on
    // screen; all you saw was "unsaved" clearing itself. The root cause was CodeEditor treating
    // the dispatch that swaps content externally as user input and sending it back (see the applying block in kinds/CodeEditor).
    const home = stateFile.slice(0, stateFile.lastIndexOf("/"));
    const file = `${home}/${kvProfile.data.file}`;
    await page
      .locator(".pane-l .role")
      .nth(0)
      .locator('input:not([type="checkbox"])')
      .first()
      .fill("m-from-control");
    await page.waitForTimeout(200);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(1200);
    const saved = fs.readFileSync(file, "utf8");
    check(
      "after saving: the role added in the raw text and the field changed in the controls are both on disk",
      saved.includes("zzprobe") && saved.includes("m-from-control"),
      JSON.stringify(saved),
    );
  }
}

// -- 12. Runtime switches are drawn as "switches", not checkboxes --
//
// The user's words were "don't do that stupid tick thing, use a switch you can toggle". The
// criterion lands on `role=switch`: screen readers and the keyboard rely on it, and a pure CSS fake switch has none.
{
  await page.locator(`nav.side button[title="plugin-manager"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="plugin-manager.switches"], nav.v button[title="plugin-manager.switches"]`)
    .first()
    .click();
  await page.waitForTimeout(300);
  const switches = await page.locator(`.card .body input[role="switch"]`).count();
  const plain = await page.locator(`.card .body input[type="checkbox"]:not([role="switch"])`).count();
  check("a switch is a switch (role=switch)", switches > 0, `actual ${switches}`);
  check("not one is a bare checkbox", plain === 0, `${plain} bare checkboxes left`);
}

// -- 13. Keyboard: `/` to find things, Esc to exit --
//
// This is the lower-bound guard for the "4-5 operations" line: by mouse, sidebar -> tab ->
// control -> save is four, with no slack; `/` and Alt+number compress "back to a known position" into one keypress.
{
  await page.keyboard.press("/");
  await page.waitForTimeout(150);
  const focused = await page.evaluate(() => document.activeElement?.className || "");
  check("`/` puts the cursor in the filter box", focused.includes("filter"), `focus is on ${JSON.stringify(focused)}`);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);
  const after = await page.evaluate(() => document.activeElement?.tagName || "");
  check("Esc exits the input", after !== "INPUT", `focus is still on ${after}`);
}

// -- 14. Under each sidebar group heading, everything really is that kind --
//
// The user's words: "shouldn't the big directories be categorized too". Grouping is
// **declared** by each module (view.Section.Group), and the sidebar gathers by it.
//
// The criterion is not "a heading was drawn" but **members really clump together**: the first
// version inserted headings as it walked, while members came in source order, so under
// the "data plane" heading there was only "breaker", and "gateway" landed under the next heading (seen live). A heading says "the rows below are one kind", so below it really must be that kind -- a caption
// added in the wrong place leaves the reader more confused.
//
// The ungrouped rows come first (today only "config"): they belong to no heading, so
// starting the cursor at the empty string lines up exactly.
{
  if (!snap.sections.some((s) => s.group)) {
    skip("no module in this assembly declares a group, skipping the sidebar grouping check");
  } else {
    const layout = await page.evaluate(() =>
      [...document.querySelector("nav.side").children]
        .filter((e) => e.classList.contains("group") || e.classList.contains("row"))
        .map((e) => ({
          head: e.classList.contains("group"),
          // Read text for headings, title for rows (= source, a machine marker that does not follow the language).
          name: e.classList.contains("group") ? e.textContent.trim() : e.getAttribute("title"),
        })),
    );
    const groupOf = new Map(snap.sections.map((s) => [s.source, s.group ?? ""]));
    let cur = "";
    let heads = 0;
    const stray = [];
    for (const r of layout) {
      if (r.head) {
        heads++;
        cur = r.name;
        continue;
      }
      if ((groupOf.get(r.name) ?? "") !== cur) stray.push(r.name);
    }
    check("the sidebar drew group headings", heads > 0, `actually drew ${heads}`);
    check(
      "under each heading there are only rows of that kind (not interrupted by another group)",
      stray.length === 0,
      `stray from their group: ${stray.join(", ")}`,
    );
  }
}

// -- 15. After a silent refresh, card order must not change --
//
// The user's words: "upstream and global settings often wander down by themselves".
// The truth is not that they "wander"; **the UI re-sorted once during a partial refresh**:
// the 3-second silent refresh asks only the Live sources, and the few cards it returns must
// be merged back into the local copy, and that merge sorted on its own -- it sorted only by `(source, id)`. So "global settings" and "upstream" with `Order` 0/1 fell below a dozen profile cards (Order 10).
//
// Why the symptom is "often" rather than "always": a full load (page reload) takes another
// path that does not sort and copies the backend order, so it is right. Only after a silent refresh is it wrong -- and that comes every 3 seconds.
//
// So this check must **wait for one real silent refresh** before comparing: @3 seconds is the interval constant in App; wait that long here.
{
  await page.locator(`nav.side button[title="config"]`).click();
  await page.waitForTimeout(300);
  const navOrder = () =>
    page.evaluate(() =>
      [...document.querySelectorAll("nav.v button.row")].map((b) => b.getAttribute("title")),
    );
  const before = await navOrder();
  check("the vertical column for the config section is drawn", before.length > 2, `actual ${before.length} cards`);
  // The backend-declared order: global settings (0) / upstream (1) before the profile cards (10).
  check(
    "the two you configure right after install come first",
    before.findIndex((id) => id === "config.providers") <
      before.findIndex((id) => id?.startsWith("config.profile.")),
    before.slice(0, 4).join(" "),
  );
  // **Auto-refresh must really be turned on**: polling is off by default (`auto = false`);
  // without turning it on this check spins idle -- it "passes" while the bug it guards
  // against was never triggered once. The first version was exactly that: revert the sort to the old code and it stayed all green.
  await page.locator('header.top input[type="checkbox"]').check();
  // Polling is every 3 seconds (the App interval); wait one cycle plus a bit more.
  await page.waitForTimeout(4200);
  const after = await navOrder();
  check(
    "order is unchanged after a silent refresh",
    JSON.stringify(after) === JSON.stringify(before),
    `before refresh ${before.slice(0, 4).join(" ")} / after refresh ${after.slice(0, 4).join(" ")}`,
  );
  check(
    "those two are still first after a silent refresh",
    after.findIndex((id) => id === "config.providers") <
      after.findIndex((id) => id?.startsWith("config.profile.")),
    after.slice(0, 4).join(" "),
  );
  await page.locator('header.top input[type="checkbox"]').uncheck();
}

// -- 16. Claude Code's slot mapping is editable, and really hits the disk --
//
// Before 2026-09-21 this mapping lived **only** in Go source (the Slots in
// `modules/claudecode/agent.go`); "make the Bash classifier use normal so a Sonnet outage does not break the whole session" meant editing code and rebuilding.
// The user's words: "right now the go code is hardcoded, damn it; I think it should be
// configurable, and the frontend should support it too".
//
// The criterion lands on **the disk**, not "the dropdown shows the new value": the latter is
// green even when only frontend state changed, and what this check wants is "the next
// takeover really uses the new role". Also lock down the trade-off that is easy to get wrong --
// rows equal to the default **are not written** (leaving same-value records would surprise whoever changes the default later, when they find their change does not apply to some users who never configured anything).
{
  await page.locator(`nav.side button[title="claudecode"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="claudecode.slots"], nav.v button[title="claudecode.slots"]`)
    .first()
    .click();
  await page.waitForTimeout(400);

  const rows = page.locator(".card .body .item");
  const n = await rows.count();
  check("the slots card has one row per slot", n >= 4, `actual ${n} rows`);

  // Find the dropdown on the sonnet row (default mid) and change it to normal.
  const sonnet = rows.filter({ has: page.locator('b:text-is("sonnet")') }).locator("select");
  const before = await sonnet.inputValue();
  check("sonnet ships with mid as the default", before === "mid", `actual ${JSON.stringify(before)}`);
  const opts = await sonnet.locator("option").allInnerTexts();
  check(
    "the dropdown holds semantic roles (not model names)",
    ["heavy", "normal", "mid", "light"].every((t) => opts.includes(t)),
    opts.join(","),
  );
  await sonnet.selectOption("normal");
  await page.waitForTimeout(200);
  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(600);

  const st = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  const slots = st.claude_slots ?? {};
  check("the changed slot landed in state.json", slots.sonnet === "normal", JSON.stringify(slots));
  check(
    "slots equal to the default are not written",
    slots.opus === undefined && slots.haiku === undefined,
    `opus=${JSON.stringify(slots.opus)} haiku=${JSON.stringify(slots.haiku)}`,
  );
  check(
    "the card says it clearly: changed now, effective on the next takeover",
    /下次接管|taken over/.test(await rows.filter({ has: page.locator('b:text-is("sonnet")') }).innerText()),
    "saying only 'saved' after the change would make the user think a running session changes at once",
  );
}

// -- 17. A client that is not installed: the whole card is locked grey --
//
// The user's words: "the frontend clients need it too -- anything not installed, lock
// it all grey". Configuring slots and toggling switches for a tool that is not installed changes not a single byte -- if the card stayed editable, the user would only learn it was wasted after doing it.
//
// Verify three things together; the rule does not hold if any is missing:
//   1. the criterion is really computed (the card carries locked, and can state a **reason** -- locking without saying why makes people guess);
//   2. the UI really draws it locked grey (a class, not "it looks grey");
//   3. the controls inside are really **disabled** (grey but not disabled, and the keyboard can still Tab in and change it).
//
// opencode is of course not installed in the sandbox (PATH points at the sandbox we built), so its section is exactly the scene.
{
  const missing = snap.sections.find((s) => s.source === "opencode-omo");
  if (!missing) {
    skip("this assembly has no opencode section, skipping check 17");
  } else {
    await page.locator(`nav.side button[title="opencode-omo"]`).click();
    await page.waitForTimeout(300);
    const card = page.locator(".card").first();
    check("the card for a client that is not installed is drawn locked grey", (await page.locator(".card.locked").count()) > 0,
      "there is no locked-grey card on screen");
    const why = await page.locator(".card.locked .locked-bar").innerText().catch(() => "");
    check("the reason for the lock is written on the card", why.trim().length > 0, "locking without saying why just makes the user think the UI is broken");
    // The reason must include **what to do**: the whole point of that field is to tell the user
    // "why it cannot move" and "how to make it move". The install command will be injected by the agent module later (`newgate <agent> -y`).
    check("the reason gives the command to install it", /newgate\s+\S+\s+-y/.test(why), `actual ${JSON.stringify(why.slice(0, 90))}`);
    const enabled = await page
      .locator(".card.locked select:not([disabled]), .card.locked input:not([disabled]), .card.locked button:not([disabled])")
      .count();
    check("no still-live controls inside a locked-grey card", enabled === 0, `${enabled} live controls left`);
    // The sidebar section must be grey too, but **clickable** -- grey states "not usable on this machine", it does not disable.
    const sec = page.locator('nav.side button[title="opencode-omo"]');
    check("the sidebar section is grey too", (await sec.getAttribute("class"))?.includes("locked") === true,
      "the sidebar is not grey, so the user thinks that section works like any other");
    check("that section is still clickable (its details must be visible)", (await sec.isEnabled()) === true,
      "disabling the whole section would hide even 'what this section is for' from the user");
  }
}

// -- 18. The codex card (takeover writes TOML, so the UI must show it too) --
//
// codex's role lives in `model = "…"` in ~/.codex/config.toml, and that file is written
// **at takeover time**. Before this card, there was nowhere to see "which role codex runs
// now" -- except typing `newgate agents` in a terminal or opening that TOML directly.
//
// The criterion is **same origin, different path** as the slots card in check 17: this one's
// value ends up in a file (not injected as an env var), so there is one extra hop between
// "changed in the UI" and "takeover really uses it". Here we lock the first half --
// **the change really hits the disk**; the second half (`newgate on codex` writing it into the TOML) is locked by modules/codex unit tests and on-device acceptance.
//
// In the sandbox codex **may or may not be installed** (depends on this machine), so do not assert locked or not here:
// the locked-grey path is already covered by check 17 with opencode, and it goes through the same criterion.
{
  const sec = page.locator(`nav.side button[title="codex"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no codex section, skipping check 18");
  } else {
    await sec.click();
    await page.waitForTimeout(300);
    const card = page.locator(".card").first();
    const sel = card.locator(".body .item").filter({ has: page.locator('b:text-is("model")') }).locator("select");
    check("the codex card has a model slot", (await sel.count()) > 0, "cannot find that dropdown on the card");

    const opts = await sel.locator("option").allInnerTexts();
    check(
      "the dropdown holds semantic roles (not model names)",
      ["heavy", "normal", "mid", "light"].every((t) => opts.includes(t)),
      opts.join(","),
    );
    const before = await sel.inputValue();
    check("codex ships with normal as the default", before === "normal", `actual ${JSON.stringify(before)}`);

    await sel.selectOption("mid");
    await page.waitForTimeout(200);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(600);

    const st = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    check("the changed role landed in codex_slots in state.json",
      (st.codex_slots ?? {}).model === "mid", JSON.stringify(st.codex_slots ?? {}));
    check(
      "the card says it clearly: effective on the next takeover",
      /下次接管|taken over/.test(await card.innerText()),
      "codex's role is written into the TOML at takeover time; without this line the user thinks a running session changes at once",
    );

    // Reverting to the default = that key disappears (same rule as check 17; verified again
    // here because it spans modules: claude and codex now share the kernel's one implementation, so a regression in either goes red here).
    await sel.selectOption("normal");
    await page.waitForTimeout(200);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(600);
    const st2 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    check("the key disappears after reverting to the default", st2.codex_slots === undefined, JSON.stringify(st2.codex_slots));
  }
}

// -- 18b. codex's mode switch (one statement plus one button on that card) --
//
// For the background (why "codex uses its own model names" at all) see the header of
// mock/e2e_codex_modes.sh. Here only the **UI half** is verified: the card states which mode
// is in effect right now, and pressing the button really changes what is written.
//
// # Why this is so short (a rule the user set on 2026-09-21)
//
//	From now on, on the ui side, tests mainly covering the BFF are enough. Touch e2e as little as possible; I will do the final review.
//
// So the behavior of that mapping table is **not here** -- it lives in modules/codex's unit
// tests (modelsview_test.go: whole-table write-back, mode must not be wiped, empty name/empty role/duplicate
// names must be rejected, a stale baseline must not overwrite others'), at millisecond
// speed, with no browser wait. Only "controls really render in the UI and a click really hits the disk" stays here -- questions only a browser can answer.
{
  const sec = page.locator(`nav.side button[title="codex"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no codex section, skipping check 18b");
  } else {
    await sec.click();
    await page.waitForTimeout(300);
    // The mode switch and that table are **on the same card** (codex.models). Open by concept
    // id, not by translated text -- wording changes; ids do not (same approach as check 6).
    await page.locator('button.tab[title="codex.models"], nav.v button[title="codex.models"]').first().click();
    await page.waitForTimeout(300);
    const card = page.locator("section.card").first();

    const note = card.locator("[data-note]");
    const tone = await note.first().getAttribute("data-note").catch(() => null);
    const text = ((await note.first().textContent().catch(() => "")) ?? "").trim();
    check("the card states which takeover mode is in effect right now",
      (await note.count()) === 1 && tone === "ok" && text.length > 0,
      `note=${await note.count()} tone=${JSON.stringify(tone)}`);

    // Only draw "the other one": drawing the current mode as a button gives a button whose click changes nothing.
    const toRename = card.locator('button[data-action="mode-rename"]');
    const toTakeover = card.locator('button[data-action="mode-takeover"]');
    const takeoverFirst = (await toTakeover.count()) === 0;
    check("only the button for 'the other' mode is drawn",
      takeoverFirst ? (await toRename.count()) === 1 : (await toTakeover.count()) === 1,
      `rename=${await toRename.count()} takeover=${await toTakeover.count()}`);

    const modelsFile = path.join(path.dirname(stateFile), "codex-models.json");
    const modeOnDisk = () => {
      try { return JSON.parse(fs.readFileSync(modelsFile, "utf8")).mode ?? ""; } catch { return ""; }
    };
    await (takeoverFirst ? toRename : toTakeover).first().click();
    await page.waitForTimeout(900);
    const after = ((await note.first().textContent().catch(() => "")) ?? "").trim();
    check("after pressing, the statement changed and it really hit the disk",
      after !== text && after.length > 0 &&
        modeOnDisk() === (takeoverFirst ? "rename" : "takeover"),
      `statement ${JSON.stringify(text)} → ${JSON.stringify(after)}, mode in file = ${JSON.stringify(modeOnDisk())}`);

    // Switch back so state is not left for later checks (this sandbox is shared).
    if (takeoverFirst) {
      await page.locator('button[data-action="mode-takeover"]').first().click();
      await page.waitForTimeout(900);
      check("pressing once more switches back", modeOnDisk() === "takeover", `mode = ${JSON.stringify(modeOnDisk())}`);
    }
  }
}

// -- 19. Language card: switch language in the web UI, effective **on the spot** --
//
// The user's request: "make a locale switch in newgate web too, matching the cli". The CLI
// one is `newgate lang <tag>`; the web one is a card contributed by the language module itself (the UI does not know "language").
//
// Verify two things together; the feature does not hold if either is missing:
//   1. after switching, **the UI really changes** (not just a file written, waiting for a restart -- the daemon is long-lived,
//      and that "clicked, nothing happened" is exactly what this guards against);
//   2. after switching back, **the distribution's strings are still there** (`i18n.Use` does not reinstall the catalog; with
//      Install, the translations the distribution Extends in at assembly time get wiped, and the symptom is "after switching to
//      Chinese that half of the UI goes back to English", which only looks like "a few strings are untranslated").
{
  const sec = page.locator(`nav.side button[title="locale"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no language section, skipping check 19");
  } else {
    await sec.click();
    await page.waitForTimeout(300);
    const card = page.locator(".card").first();
    // Take the language field by **position**, not by label text: its label is translated (whatever
    // language this UI speaks right now, that is the word), and using a translated word as a
    // selector makes this check depend on "which language it runs in" -- while the entire content of this check is switching languages.
    const sel = card.locator(".body .item select").first();
    check("the language card has that selector", (await sel.count()) > 0, "cannot find the language dropdown on the card");
    const opts = await sel.locator("option").allInnerTexts();
    check("the dropdown holds language tags", opts.includes("en") && opts.includes("zh-Hans"), opts.join(","));

    // What the "config" row on the sidebar is called right now; we will compare against it shortly.
    const configTitleBefore = await page.locator(`nav.side button[title="config"]`).innerText();

    await sel.selectOption("en");
    await page.waitForTimeout(200);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(900);

    const configTitleEn = await page.locator(`nav.side button[title="config"]`).innerText();
    check("after switching to en the UI turns English on the spot", /Configuration/i.test(configTitleEn),
      `the sidebar row is still called ${JSON.stringify(configTitleEn)} -- saved but not effective`);

    // Switch back, and along the way verify "the distribution's translations were not wiped": the name of the
    // `claudecode` row comes from the **distribution** catalog (the kernel does not know it); if it goes back to English,
    // then Use was written as Install (reinstalling the catalog).
    await sel.selectOption("zh-Hans");
    await page.waitForTimeout(200);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(900);

    const configTitleBack = await page.locator(`nav.side button[title="config"]`).innerText();
    check("after switching back to Chinese the UI follows back", configTitleBack === configTitleBefore,
      `${JSON.stringify(configTitleBefore)} → ${JSON.stringify(configTitleBack)}`);
    const ccTitle = await page.locator(`nav.side button[title="claudecode"]`).innerText();
    check("the distribution's own translations survive the language switch", ccTitle.trim().length > 0 && ccTitle.trim() !== "claudecode",
      `the claudecode row shows ${JSON.stringify(ccTitle)} -- the distribution catalog was wiped by a reinstall`);

    const st = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    check("the language landed in state.json", (st.module_config ?? {}).locale?.lang === "zh-Hans",
      JSON.stringify((st.module_config ?? {}).locale));
  }
}

// -- 20. Chain head: which profile each client starts from --
//
// The user's request: "add per-agent state to state... optimize the web". The per-agent
// table (`active` in state.json) has existed since M1, but **only the CLI had it** (`newgate --set-profile
// <name> --agent <x>`) -- the browser could change which role each slot of this client
// uses, but not where its whole chain starts, and the latter comes first: the chain decides which provider's model each role lands on.
//
// Verify three things together; the feature does not hold if any is missing:
//   1. the field really renders, and the options include **empty** (= follow the global default, not "no choice");
//   2. choose a real profile → lands in `active` in state.json;
//   3. choosing empty again → that key **disappears** (leaving `{"codex": ""}` is a record nobody can read,
//      the same rule as the "same-value record" in check 18).
{
  const sec = page.locator(`nav.side button[title="codex"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no codex section, skipping check 20");
  } else {
    await sec.click();
    await page.waitForTimeout(300);
    const card = page.locator(".card").first();
    // Take the first field by position: labels are translated (this check does not switch
    // languages, but others do), and **the chain head is always the first field** -- that order has meaning (the chain comes before the roles), so it deserves pinning.
    const sel = card.locator(".body .item").first().locator("select");
    check("the first field is the chain-head selector", (await sel.count()) > 0, "there is no dropdown in the first field");

    const opts = await sel.locator("option").allInnerTexts();
    // The first option is "follow the global default", and it **must name whom it follows** -- the user's
    // words were "you have to distinguish whether the user chose xx or xx is the default". Both have
    // the same current value in this field (`Active()` returns the one actually used now), so the criterion can only land on **what is displayed**:
    // the follow-default option carries the word "default", while when the user really chose one, that option is just a bare name.
    check("the first chain-head option is 'follow the global default' and names whom it follows",
      /缺省|default/.test(opts[0] ?? ""), `first option shows ${JSON.stringify(opts[0])}`);
    check("the chain-head options include real profile names",
      opts.slice(1).some((o) => o && !/缺省|default/.test(o)), opts.join(","));

    // Pick a profile **not equal to the global default**: same name as the default = never set (see
    // confighook.AgentProfile.Write), so using it to verify "landed on disk" would verify that rule
    // instead of this one. In the sandbox `demo` happens to be the default, which is exactly why the first version went red.
    const st0 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    const other = opts.slice(1).find((o) => o && !/缺省|default/.test(o) && o !== st0.default_profile);
    if (!other) {
      skip("this sandbox has nothing besides the default profile, skipping the chain-head check");
    } else {
      await sel.selectOption(other);
      await page.waitForTimeout(200);
      await page.locator("header.top button.primary").click();
      await page.waitForTimeout(600);

      const st = JSON.parse(fs.readFileSync(stateFile, "utf8"));
      check("choosing a profile landed in active in state.json",
        (st.active ?? {}).codex === other, JSON.stringify(st.active ?? {}));

      await sel.selectOption("");
      await page.waitForTimeout(200);
      await page.locator("header.top button.primary").click();
      await page.waitForTimeout(600);
      const st2 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
      check("the key disappears after choosing empty again (not left as an empty string)",
        (st2.active ?? {}).codex === undefined, JSON.stringify(st2.active ?? {}));
    }
  }
}

// -- 21. The "test" button on each row of the breaker table --
//
// The user's request: "add a test button to each item on the breaker page (pick a reasonable
// icon); clicking it can test the probe. Ultimately it should update the trip removal, latency, etc."
//
// The point of this check is **not** "the button is drawn" but the last half, "ultimately it should
// update". So verify three things together; the feature does not hold if any is missing:
//   1. there really is a button on that row (none = the UI does not offer the action at all);
//   2. clicking **really sends it**: the sandbox provider points at a dead port, so the connection
//      must fail; the banner must name **which binding** and include the reason. "Nothing happened" is the worst answer here
//      -- the user would think the button is broken;
//   3. those columns in the table **really change**: the result enters the health table (probe, failure count, cooldown), not
//      just a sentence popped up in the UI and nothing more.
//
// Find the button by `data-action`, not by its label: the label is given by the contributor (translated by the backend; in this
// sandbox it is "⚡ test"), and using it as a selector makes this check follow the language.
{
  const sec = page.locator(`nav.side button[title="breaker"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no breaker section, skipping check 21");
  } else {
    await sec.click();
    await page.waitForTimeout(300);
    const card = page.locator("section.card").first();
    const btn = card.locator('button[data-action="probe"]').first();

    if ((await btn.count()) === 0) {
      // No rows at all: the failing request from ui_check.sh did not create a row (no breaker in the
      // assembly, or the classification changed). Skip rather than fail -- this check verifies the
      // button, not "the sandbox can always produce a bad binding".
      skip("no rows in the health table, skipping check 21 (did the ui_check.sh request fail to create one?)");
    } else {
      const row = card
        .locator("tbody tr")
        .filter({ has: page.locator('button[data-action="probe"]') })
        .first();
      const squeeze = (t) => t.replace(/\s+/g, " ").trim();
      const before = squeeze(await row.innerText());

      await btn.click();
      // The probe has to hit that dead port and come back (connection refused is immediate, but this call waits synchronously).
      await page.waitForTimeout(2000);

      const bannerText = await page.locator(".banner").first().innerText().catch(() => "");
      check(
        "after clicking the test button, the banner names that binding",
        /demo\/demo-model/.test(bannerText),
        `banner says ${JSON.stringify(bannerText.slice(0, 160))}`,
      );
      check(
        "the banner gives the reason it cannot be reached (not 'nothing happened')",
        bannerText.length > "demo/demo-model".length + 4,
        `banner says ${JSON.stringify(bannerText.slice(0, 160))}`,
      );

      const after = squeeze(await row.innerText());
      check(
        "the probe result entered the health table (columns in that row changed)",
        after !== before,
        `before the click ${JSON.stringify(before)} / after the click ${JSON.stringify(after)}`,
      );
    }
  }
}

// -- 22. "Apply / apply to all": make one profile the global default --
//
// The user's request: "add one more feature to config: when on a specific kv detail page, add a
// button that can make the current config the default, plus a force-apply button as well."
//
// The wording was set by him later (original words: "you should make it apply and apply all. Apply. Apply to all.")
// -- the first version wrote `set as the default profile`, the second wrote `force: …`, and `force`
// is **his spoken shorthand when describing the requirement**, not a word on the UI. So find the button by `data-action`,
// touching not a single word: wording is a product decision and changes; machine markers do not.
//
// Verify four things together:
//   1. on the non-default card **both** buttons are present;
//   2. click "apply to all" -> it becomes the global default, and every client's chain head is cleared;
//   3. once it is the default, that card **has no buttons** -- "a button proposing to do what is already done,
//      standing next to a row of buttons that really do things, is noise" (see profileActions in config/view.go);
//   4. and that card **has a green "current config" line** (see below).
//
// # Item 4 was added later (user, 2026-09-21)
//
// Originally there was only item 3, so that card showed **nothing**. The user's words:
//
//	What you should inject is an info-only green: current config / the effect of the Active profile!
//
// He is right: with those two buttons gone, the two cards look nearly identical and the user
// cannot tell which one is in effect -- and that is exactly what he opens this section to learn. **"Not drawing that button" and "saying nothing" are two different things**;
// item 3 only pinned the first, so this gap could slip through entirely. Now both are pinned together.
{
  const sec = page.locator(`nav.side button[title="config"]`);
  if ((await sec.count()) === 0) {
    skip("this assembly has no config section, skipping check 22");
  } else {
    await sec.click();
    await page.waitForTimeout(250);
    const st0 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    const def = st0.default_profile;
    const target = snap.concepts.find(
      (c) => c.id.startsWith("config.profile.") && c.id !== `config.profile.${def}`,
    );
    if (!target) {
      skip("this sandbox has only one profile, skipping check 22");
    } else {
      const name = target.id.slice("config.profile.".length);
      // Open a given card: try both the horizontal tab strip and the left vertical column (which
      // one this section uses depends on how many cards it has -- see check 6). Same approach as openSwitches.
      const open = async (id) => {
        await page.locator(`button.tab[title="${id}"], nav.v button[title="${id}"]`).first().click();
        await page.waitForTimeout(250);
      };
      await open(target.id);
      const card = page.locator("section.card").first();
      const applyOne = card.locator('button[data-action="set-default"]');
      const applyAll = card.locator('button[data-action="set-default-all"]');
      check("the non-default profile card has both an 'apply' and an 'apply to all' button",
        (await applyOne.count()) === 1 && (await applyAll.count()) === 1,
        `apply=${await applyOne.count()} applyAll=${await applyAll.count()}`);
      // That line must **not** be there right now -- it says "is this the current config", and at this moment it is not.
      // It must be asked here, not later: `card` is a live locator (it re-resolves against the current page on every query);
      // after switching to the default one it points at the default card, so the answer would no longer be
      // about the same card (the first version got this wrong, and this check caught it: `note=1 on the non-default card`).
      check("the non-default card does not carry that line (it is a verdict; on an inactive card it would be a lie)",
        (await card.locator("[data-note]").count()) === 0,
        `note on the non-default card=${await card.locator("[data-note]").count()}`);

      // The default card must not have it -- jump over to confirm (this step also verifies "both sides render").
      await open(`config.profile.${def}`);
      const defCard = page.locator("section.card").first();
      check("the card that is already the default has neither button",
        (await defCard.locator('button[data-action="set-default"]').count()) === 0 &&
          (await defCard.locator('button[data-action="set-default-all"]').count()) === 0,
        "a button that proposes to do what is already done drowns out the buttons next to it that really do things");

      // And it **must say it**: a green "current config" line. Find it by `data-note` (a machine marker),
      // not by translated text -- wording changes; markers do not (same reason as finding buttons by data-action at the start of this section).
      // Verify the tone too: it is a **statement**; drawn as a warning or error it would be saying "something went wrong".
      const defNote = defCard.locator("[data-note]");
      const defNoteTone = await defNote.first().getAttribute("data-note").catch(() => null);
      const defNoteText = ((await defNote.first().textContent().catch(() => "")) ?? "").trim();
      check("the card that is already the default has a green 'current config' line",
        (await defNote.count()) === 1 && defNoteTone === "ok" && defNoteText.length > 0,
        `note=${await defNote.count()} tone=${JSON.stringify(defNoteTone)} text=${JSON.stringify(defNoteText)}`);

      // Go back to the target card and click "apply to all".
      await open(target.id);
      await card.locator('button[data-action="set-default-all"]').first().click();
      await page.waitForTimeout(900);

      const st = JSON.parse(fs.readFileSync(stateFile, "utf8"));
      check("after clicking 'apply to all' it became the global default", st.default_profile === name,
        `the default is still ${JSON.stringify(st.default_profile)}`);
      check("every client's chain head is cleared (no `{\"claude\": \"\"}` records left)",
        Object.keys(st.active ?? {}).length === 0, JSON.stringify(st.active ?? {}));

      // After the re-read that card is the default -- the buttons should vanish by themselves and the
      // line should appear by itself. Verify both: checking only that buttons vanished would pass "this card now says nothing", which is exactly the original bug.
      const after = page.locator("section.card").first();
      check("after it became the default, the buttons on that card vanished by themselves",
        (await after.locator('button[data-action="set-default-all"]').count()) === 0,
        "the buttons are still there after the snapshot re-read, so it is reading the old one");
      check("after it became the default that line appeared by itself (buttons replaced by a statement, not both gone)",
        (await after.locator("[data-note]").count()) === 1,
        `note on the card that became the default=${await after.locator("[data-note]").count()}`);
    }
  }
}

check("no page errors in the whole run", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

console.log(`\nresult: ${pass} passed, ${fail} failed`);
await browser.close();
process.exit(fail === 0 ? 0 : 1);
