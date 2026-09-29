// 皮肤怎么落到 DOM 上。
//
// # 为什么是一段注入的 <style> 而不是 class
//
// 后端给的是**一串令牌**（`--bg: …; --accent: …;`），不是一组 class 名。走 class
// 的话，样式表里得为每一套皮肤各写一段，而「有哪些皮肤」是**装配期**的事实（装哪
// 几个 theme-* 模块）——前端写死一份名单，就等于每加一套皮肤都要改一次前端，
// 而那样一来「删掉皮肤模块界面照常工作」这条也就不成立了。
//
// 注入的样式表按 `[data-theme="<id>"]` 限定：令牌只覆盖**这个 id 生效时**，切走就
// 整段失效，不需要谁去把上一次的覆盖清掉。
//
// # 为什么 data-theme 挂在 <html> 上
//
// app.css 里那套出厂令牌写在 `:root`（= html），而它那个浅色分支写在
// `@media (prefers-color-scheme: light)` 里。同一个元素上两套规则时，胜出的是
// **具体度更高**的那个：`html[data-theme="x"]` 比 `:root:not([data-theme])` 高，
// 所以选了皮肤就是选了皮肤——系统偏好不再插手。这也是为什么 app.css 那个媒体查询
// 必须带上 `:not([data-theme])`（见那里）。
//
// # color-scheme
//
// 滚动条、复选框的勾、自动填充的底色不归 CSS 变量管，它们跟着 `color-scheme` 走。
// 所以深色皮肤要把它设成 dark，否则一套真黑界面会挂着一条白滚动条。这件事只能由
// **皮肤自己声明**（后端的 Theme.Dark）——从前端颜色的亮度里猜，会在「一套很浅的
// 暖色皮肤」上猜错。

import type { ThemeInfo, Themes } from "./api";

/** STYLE_ID 是那段注入样式表的 id（换皮肤时先找到它、再替换内容）。 */
const STYLE_ID = "ng-theme";

/**
 * applyTheme 把当前这套皮肤落到文档上。
 *
 * `active` 为空 = 出厂那套令牌：这时**什么都不注入**、并且把 `data-theme` 去掉——
 * 于是 app.css 的 `:root` 与它那个 `prefers-color-scheme` 分支重新生效。这是
 * 「跟随系统」那个状态，它不是一个皮肤模块。
 */
export function applyTheme(doc: Themes): void {
  const root = document.documentElement;
  const active: ThemeInfo | undefined = doc.themes.find((x) => x.id === doc.active);

  let el = document.getElementById(STYLE_ID) as HTMLStyleElement | null;
  if (!active) {
    el?.remove();
    root.removeAttribute("data-theme");
    // 交给系统：`color-scheme: light dark` 是 app.css 里那个默认值。
    root.style.removeProperty("color-scheme");
    return;
  }

  if (!el) {
    el = document.createElement("style");
    el.id = STYLE_ID;
    // 放 head 末尾：它只覆盖令牌，但万一哪天有人往令牌里写了没见过的选择器，
    // 位置靠后至少让它的行为是可预期的。
    document.head.append(el);
  }
  // 令牌只在这个 id 生效时覆盖（见文件头）。选择器由这里拼，皮肤只给令牌
  // ——皮肤能写选择器的话，「这行样式是谁加的」就要靠读一段运行时字符串才能回答。
  el.textContent = `:root[data-theme="${active.id}"] {${active.css}}`;
  root.setAttribute("data-theme", active.id);
  root.style.setProperty("color-scheme", active.dark ? "dark" : "light");
}
