<script lang="ts">
  // 一张卡片 = 一个概念。它只做三件事：按 Kind 挑渲染器、显示「读不出来」的原因、
  // 标出「这张有没保存的改动」。
  //
  // 它**不认识任何模块**：`source` 只是个用来分组的字符串。加一个模块的界面意味着
  // 加一个 Kind 的渲染器（或者复用已有的），而不是在这张卡片里加一个 if。
  import type { Concept } from "./api";
  import { t } from "./i18n";
  import CodeEditor from "./kinds/CodeEditor.svelte";
  import MappingEditor from "./kinds/MappingEditor.svelte";
  import Toggles from "./kinds/Toggles.svelte";
  import Series from "./kinds/Series.svelte";
  import LogView from "./kinds/LogView.svelte";
  import Table from "./kinds/Table.svelte";

  let {
    concept,
    draft,
    onEdit,
    onRevert,
  }: {
    concept: Concept;
    draft: unknown;
    onEdit: (v: unknown) => void;
    onRevert: () => void;
  } = $props();

  const isDirty = $derived(draft !== undefined);

  function changed(v: unknown) {
    onEdit(v);
  }
</script>

<section class="card" class:dirty={isDirty} class:broken={!!concept.error}>
  <header>
    <h3>{concept.title}</h3>
    <span class="meta">{concept.id}</span>
    <span class="spacer"></span>
    {#if isDirty}
      <span class="pill">{t("unsaved")}</span>
      <button class="tiny ghost" onclick={onRevert}>{t("revert")}</button>
    {/if}
    {#if !concept.writable && !concept.error}
      <span
        class="pill"
        title={t("the contributor offers no way to write this one back (it may hold credentials)")}
      >
        {t("read-only")}
      </span>
    {/if}
  </header>

  <div class="body">
    {#if concept.error}
      <!-- 读不出来也要占一张卡片：不报它，用户会以为这东西不存在。 -->
      <p class="dim">{concept.error}</p>
    {:else if concept.kind === "mapping-editor"}
      <MappingEditor data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "code"}
      <CodeEditor data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "toggles"}
      <Toggles data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "table"}
      <Table data={concept.data} />
    {:else if concept.kind === "series"}
      <Series data={concept.data} />
    {:else if concept.kind === "log"}
      <LogView data={concept.data} />
    {:else}
      <!-- 没有渲染器的 Kind（table / log / 将来加的）：把原文摆出来，而不是
           假装它不存在。加渲染器是前端的事，不该由后端等。 -->
      <p class="dim">{t("no renderer for kind “{kind}” yet — raw data:", { kind: concept.kind })}</p>
      <pre class="raw">{JSON.stringify(concept.data, null, 2)}</pre>
    {/if}
  </div>
</section>

<style>
  .raw {
    font-family: var(--mono);
    font-size: 12px;
    max-height: 320px;
    overflow: auto;
    margin: 0;
  }
</style>
