<script lang="ts">
  // Keyboard. **One global listener**, and what it produces is an event (see Action in nav.ts), not "which
  // button was clicked" - App receives that event and decides what happens.
  //
  // Why it exists: this UI's acceptance bar is "anything reachable within 4-5 operations", and by mouse going
  // sidebar -> tab -> control -> save is 4, with no slack. The keyboard compresses "get back to a known place" into
  // one keystroke (Alt+digit jumps to a section, `[`/`]` switch cards within a section, `/` finds things, Cmd+S saves).
  //
  // # When **not** to intercept
  //
  // A single-key shortcut is a plain character inside an input box: not being able to type a square bracket into a config file is the worst thing
  // this kind of feature can do, and nobody would connect it to a shortcut. So except for Cmd+S and Esc, everything first goes through
  // shortcutAllowed (see nav.ts) - it recognizes input boxes, dropdowns, contenteditable and CodeMirror.
  import { shortcutAllowed, type Action } from "./nav";

  let { onAction }: { onAction: (a: Action) => void } = $props();

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      const mod = e.ctrlKey || e.metaKey;
      // Save and "leave the input" are intercepted **unconditionally**: inside an editor they also have only one meaning,
      // and if Cmd+S is not intercepted the browser pops up the "save web page" dialog.
      if (mod && (e.key === "s" || e.key === "S")) {
        e.preventDefault();
        onAction({ kind: "save" });
        return;
      }
      if (e.key === "Escape") {
        (document.activeElement as HTMLElement | null)?.blur?.();
        onAction({ kind: "blur" });
        return;
      }
      if (!shortcutAllowed(e.target)) return;
      if (mod || e.altKey) {
        // Alt+digit: jump to section N. Use e.code rather than e.key - under many keyboard layouts
        // Alt+digit produces a different character (on macOS it is ¡™£…).
        const m = /^Digit([1-9])$/.exec(e.code);
        if (m && e.altKey) {
          e.preventDefault();
          onAction({ kind: "section", index: Number(m[1]) - 1 });
        }
        return;
      }
      switch (e.key) {
        case "/":
          e.preventDefault();
          onAction({ kind: "focus-filter" });
          break;
        case "[":
          e.preventDefault();
          onAction({ kind: "card", delta: -1 });
          break;
        case "]":
          e.preventDefault();
          onAction({ kind: "card", delta: 1 });
          break;
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>
