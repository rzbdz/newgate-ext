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
  import Records from "./kinds/Records.svelte";
  import Toggles from "./kinds/Toggles.svelte";
  import Series from "./kinds/Series.svelte";
  import LogView from "./kinds/LogView.svelte";
  import Table from "./kinds/Table.svelte";

  let {
    concept,
    draft,
    onEdit,
    onRevert,
    onDeleteFile,
  }: {
    concept: Concept;
    draft: unknown;
    onEdit: (v: unknown) => void;
    onRevert: () => void;
    // 删这一份文件（不带参数：作用在这张卡自己身上）。
    onDeleteFile?: () => void;
  } = $props();

  const isDirty = $derived(draft !== undefined);

  function changed(v: unknown) {
    onEdit(v);
  }
</script>

<!-- `fills`：这一种 Kind 要占满可用高度（今天只有 log，理由见 app.css）。判据挂在
     卡片上而不是让 CSS 去猜，因为「我要多高」是**这一种 Kind 的属性**，不是它碰巧
     画出来的形状。 -->
<section
  class="card"
  class:dirty={isDirty}
  class:broken={!!concept.error}
  class:fills={concept.kind === "log" && !concept.error}
>
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

  <!-- `{#key concept.id}`：**换一张卡就把渲染器整个重建**。
       MappingEditor / Toggles / Records 都各有一份「我改过没有」的本地状态
       （`edited`），而它优先于 props 与 draft。Svelte 在同一个位置复用组件实例，
       所以切卡时那份本地状态会**跟着活下来**——于是「在 A 档改了没保存 → 切到 B
       档」会看到 B 的文件名配 A 的档位，再点保存就把 A 写进了 B 的文件。
       加 key 之后本地状态随卡重建；而**真正的未保存改动不会丢**——它在 App 的
       `drafts`（按概念 id 存）里，切回去照样在。 -->
  <div class="body">
   {#key concept.id}
    {#if concept.error}
      <!-- 读不出来也要占一张卡片：不报它，用户会以为这东西不存在。 -->
      <p class="dim">{concept.error}</p>
    {:else if concept.kind === "mapping-editor"}
      <MappingEditor
        data={concept.data}
        {draft}
        readonly={!concept.writable}
        onEdit={changed}
        {onDeleteFile}
      />
    {:else if concept.kind === "code"}
      <CodeEditor data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "toggles"}
      <Toggles data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "records"}
      <Records data={concept.data} {draft} readonly={!concept.writable} onEdit={changed} />
    {:else if concept.kind === "table"}
      <Table data={concept.data} />
    {:else if concept.kind === "series"}
      <Series data={concept.data} />
    {:else if concept.kind === "log"}
      <LogView data={concept.data} />
    {:else}
      <!-- 还没有渲染器的 Kind（将来加的那种）：把原文摆出来，而不是假装它不
           存在。加渲染器是前端的事，不该由后端等。
           （2026-09-20 更正：这条注释原来把 table 与 log 也列成没渲染器的，
           那两位各自的分支就在上面十几行、渲染器也都在 kinds/ 里。） -->
      <p class="dim">{t("no renderer for kind “{kind}” yet — raw data:", { kind: concept.kind })}</p>
      <pre class="raw">{JSON.stringify(concept.data, null, 2)}</pre>
    {/if}
   {/key}
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
