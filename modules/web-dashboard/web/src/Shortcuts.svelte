<script lang="ts">
  // 键盘。**一条全局监听**，按出来的是一件事（见 nav.ts 的 Action），不是「哪个
  // 按钮被点了」——App 拿到那件事再决定发生什么。
  //
  // 为什么要有它：这个界面的验收线是「任何东西 4-5 次操作内到达」，而鼠标走完
  // 侧栏 → tab → 控件 → 保存是 4 次，没有余量。键盘把「回到某个已知位置」压缩成
  // 一次按键（Alt+数字 直达某一节、`[`/`]` 在节内换卡、`/` 找东西、⌘S 存）。
  //
  // # 什么时候**不**接
  //
  // 单键快捷键在输入框里是普通字符：在配置文件里打不出一个方括号，是这类功能最
  // 容易造成的坏，而且没人会联想到快捷键身上。所以除 ⌘S 与 Esc 外，一律先过
  // shortcutAllowed（见 nav.ts）——它认输入框、下拉、contenteditable 与 CodeMirror。
  import { shortcutAllowed, type Action } from "./nav";

  let { onAction }: { onAction: (a: Action) => void } = $props();

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      const mod = e.ctrlKey || e.metaKey;
      // 保存与「退出输入」是**无条件**接的：它们在编辑器里也只有一个意思，
      // 而且 ⌘S 不拦下来的话浏览器会弹「保存网页」对话框。
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
        // Alt+数字：直达第 N 节。用 e.code 而不是 e.key——很多键盘布局下
        // Alt+数字打出的是别的字符（macOS 上是 ¡™£…）。
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
