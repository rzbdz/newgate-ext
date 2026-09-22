<script lang="ts">
  // 演示页的外壳。
  //
  // # 它为什么不复用 App.svelte
  //
  // App.svelte 要全套后端：snapshot / apply / preview 三个端点、每 3 秒一次的自动
  // 刷新、冲突对话框、跨节过滤、快捷键。这里全都不需要——需要的只有「几张卡片 +
  // 一点本地状态」。硬塞进 App 的结果是给产品那条路加一堆「如果没后端就……」的
  // 分支，而那些分支一年也跑不到一次（它们只在演示页上跑）。
  //
  // # 它复用的是**卡片本身**
  //
  // ConceptCard 与七个 kinds 是纯的（给数据就画，没有一处 fetch），所以这里直接
  // 用它们——屏幕上那几张卡与产品里是**同一个组件、同一份 CSS**。这正是这个演示
  // 页值得存在的地方：它不是一张截图，也不是照着重画的一版。
  //
  // # 假的那一半
  //
  // 编辑、切模式、加删行都在本地 state 里真生效；点「应用」只弹一句「这是演示」。
  // **一行都不落盘**——这里没有后端可落。
  import ConceptCard from "../src/ConceptCard.svelte";
  import { t } from "../src/i18n";
  import { d } from "./strings";
  import type { Concept, ConceptAction, RowAction } from "../src/api";
  import { snapshot, useDemoLanguage, type Mode } from "./data";

  useDemoLanguage();

  let mode = $state<Mode>("takeover");
  let active = $state<string>("codex.model");
  /** 本地草稿：按概念 id 存，与产品那边 App 的 `drafts` 同一条路。 */
  let drafts = $state<Record<string, unknown>>({});
  let toast = $state<string>("");

  const snap = $derived(snapshot(mode));
  const cards = $derived(snap.concepts);
  const current = $derived(cards.find((c) => c.id === active) ?? cards[0]);

  function say(msg: string) {
    toast = msg;
    // 提示自己消失：演示页不该留下一个要手动关的东西（它只是说一句「这里没有
    // 后端」，不是一条要用户处理的错误）。
    setTimeout(() => (toast = ""), 2600);
  }

  /** 切卡（换一张就把草稿清掉——与产品那边切卡的行为一致）。 */
  function pick(id: string) {
    active = id;
    drafts = {};
  }

  function onEdit(id: string, v: unknown) {
    drafts = { ...drafts, [id]: v };
  }

  function onRevert(id: string) {
    const next = { ...drafts };
    delete next[id];
    drafts = next;
  }

  /**
   * 一个动作：**模式那个真的生效**，其余的一律弹「没有后端」。
   *
   * 模式那一支值得特殊对待，因为它是这个演示页唯一想让人看见的东西——切过去之后
   * 上面那张档位卡的取值会跟着从档位名变成 codex 的模型名，而下面那张表的说明也
   * 跟着变。那正是「两种模式到底差在哪」的答案，值得让人自己点一次。
   */
  function onAction(c: Concept, a: ConceptAction) {
    if (c.id === "codex.models" && (a.id === "mode-rename" || a.id === "mode-takeover")) {
      mode = a.id === "mode-rename" ? "rename" : "takeover";
      drafts = {}; // 换模式会换掉上面那张卡的取值，草稿跟着作废
      say(
        d(
          mode === "rename"
            ? "demo: switched to rename mode (the real one rewrites config.toml; only the screen changed here)"
            : "demo: switched back to takeover mode (the real one rewrites config.toml; only the screen changed here)",
        ),
      );
      return;
    }
    say(d("this is a demo — there is no daemon behind it, so nothing is written"));
  }

  function onRowAction(row: string, a: RowAction) {
    say(
      d("this is a demo — “{action}” on “{row}” would talk to the real daemon", {
        action: a.label,
        row,
      }),
    );
  }

  function onDeleteFile() {
    say(d("this is a demo — no file would be deleted"));
  }
</script>

<div class="page">
  <nav class="tabs">
    {#each cards as c (c.id)}
      <button class="tab" class:on={c.id === active} title={c.id} onclick={() => pick(c.id)}>
        {c.title}
      </button>
    {/each}
  </nav>

  <div class="body">
    {#key active}
      <ConceptCard
        concept={current}
        draft={drafts[current.id]}
        onEdit={(v) => onEdit(current.id, v)}
        onRevert={() => onRevert(current.id)}
        {onDeleteFile}
        onAction={(a) => onAction(current, a)}
        {onRowAction}
      />
    {/key}
    <div class="row savebar">
      <span class="dim">
        {Object.keys(drafts).length ? t("unsaved") : d("nothing changed yet")}
      </span>
      <span class="spacer"></span>
      {#if Object.keys(drafts).length}
        <button class="ghost" onclick={() => onRevert(current.id)}>{t("revert")}</button>
      {/if}
      <button class="primary" onclick={() => onAction(current, { id: "apply", label: t("save") })}>
        {t("save")}
      </button>
    </div>
  </div>

  {#if toast}
    <div class="toast">{toast}</div>
  {/if}
</div>
