// How a skin lands on the DOM.
//
// # Why an injected <style> rather than classes
//
// What the backend gives is **a string of tokens** (`--bg: …; --accent: …;`), not a set of
// class names. With classes, the stylesheet would need one block per skin, while "which
// skins there are" is an **assembly-time** fact (which theme-* modules are installed) --
// hard-coding a list in the front end would mean editing the front end for every new
// skin, and then "removing the skin module leaves the UI working" would stop holding too.
//
// The injected stylesheet is scoped by `[data-theme="<id>"]`: tokens override **only while
// this id is in effect**, and switching away invalidates the whole block, so nobody has to
// clear the previous override.
//
// # Why data-theme hangs on <html>
//
// The factory tokens in app.css are written on `:root` (= html), and their light branch is
// written inside `@media (prefers-color-scheme: light)`. With two sets of rules on the same
// element, the **more specific** one wins: `html[data-theme="x"]` beats
// `:root:not([data-theme])`, so picking a skin is picking a skin -- the system preference no
// longer interferes. That is also why the media query in app.css must carry
// `:not([data-theme])` (see there).
//
// # color-scheme
//
// Scrollbars, checkbox ticks, autofill backgrounds are not governed by CSS variables; they
// follow `color-scheme`. So a dark skin must set it to dark, otherwise a truly black UI
// hangs a white scrollbar. This can only be **declared by the skin itself** (the backend's
// Theme.Dark) -- guessing from the brightness of the front-end colors gets it wrong on "a
// very light warm skin".

import type { ThemeInfo, Themes } from "./api";

/** STYLE_ID is the id of that injected stylesheet (switching skins finds it first, then
    replaces its content). */
const STYLE_ID = "ng-theme";

/**
 * applyTheme lands the current skin on the document.
 *
 * `active` empty = the factory tokens: then **nothing is injected** and `data-theme` is
 * removed -- so app.css's `:root` and its `prefers-color-scheme` branch take effect again.
 * That is the "follow the system" state, and it is not a skin module.
 */
export function applyTheme(doc: Themes): void {
  const root = document.documentElement;
  const active: ThemeInfo | undefined = doc.themes.find((x) => x.id === doc.active);

  let el = document.getElementById(STYLE_ID) as HTMLStyleElement | null;
  if (!active) {
    el?.remove();
    root.removeAttribute("data-theme");
    // Hand it to the system: `color-scheme: light dark` is the default in app.css.
    root.style.removeProperty("color-scheme");
    return;
  }

  if (!el) {
    el = document.createElement("style");
    el.id = STYLE_ID;
    // Append to the end of head: it only overrides tokens, but should someone someday put
    // an unseen selector into a token, being last at least makes its behavior predictable.
    document.head.append(el);
  }
  // Tokens override only while this id is in effect (see the file header). The selector is
  // assembled here, the skin only gives tokens -- if a skin could write selectors, "who
  // added this line of style" would require reading a runtime string to answer.
  el.textContent = `:root[data-theme="${active.id}"] {${active.css}}`;
  root.setAttribute("data-theme", active.id);
  root.style.setProperty("color-scheme", active.dark ? "dark" : "light");
}
