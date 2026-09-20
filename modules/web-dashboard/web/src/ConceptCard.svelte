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
    preview,
    onEdit,
    onRevert,
    onDeleteFile,
  }: {
    concept: Concept;
    draft: unknown;
    /**
     * 「同一份文件**另一半**的草稿长这样时，这张卡该显示成什么」（见 App.svelte
     * 的 previews 与内核的 Concept.Preview）。有它就用它，没有就用快照里那份。
     *
     * 走 data 这条路（而不是另开一个 prop 给渲染器）是刻意的：渲染器本来就只认
     * `data` 一个形状，多一条路等于每个渲染器都要再学一遍「两份数据谁优先」。
     * 而且它**换了新对象**这件事本身就够渲染器清掉自己那份本地编辑缓存了
     * （见 MappingEditor 里 `seen === data` 那个 effect）——那正是我们要的：
     * 原文改过之后，控件那一半必须重新照着新内容画。
     */
    preview?: unknown;
    onEdit: (v: unknown) => void;
    onRevert: () => void;
    // 删/建这一份文件（不带参数：作用在这张卡自己身上，见 kinds/MappingEditor）。
    onDeleteFile?: () => void;
  } = $props();

  const isDirty = $derived(draft !== undefined);
  /** 渲染器看到的那份数据：草稿预览优先于快照。 */
  const shown = $derived(preview !== undefined ? preview : concept.data);

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
  class:locked={!!concept.locked}
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
    {#if concept.locked}
      <!-- 锁灰的理由就在卡片头上：整张卡禁掉了，不说为什么等于让用户猜。 -->
      <span class="pill locked-pill" title={concept.locked}>{t("locked")}</span>
    {:else if !concept.writable && !concept.error}
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
  {#if concept.locked}
    <!-- 锁死的卡**照常画出来**：藏掉的话用户会以为那个功能不存在，而真相是
         「它在，只是这台机器上用不上」。控件全部禁掉（见下面 readonly 那条），
         这一条横幅说清「为什么」与「怎么办」——只灰不说，用户只会以为界面坏了。 -->
    <div class="locked-bar">{concept.locked}</div>
  {/if}

  <div class="body">
   {#key concept.id}
    {#if concept.error}
      <!-- 读不出来也要占一张卡片：不报它，用户会以为这东西不存在。 -->
      <p class="dim">{concept.error}</p>
    {:else if concept.kind === "mapping-editor"}
      <MappingEditor
        data={shown}
        {draft}
        readonly={!concept.writable || !!concept.locked}
        onEdit={changed}
        {onDeleteFile}
      />
    {:else if concept.kind === "code"}
      <CodeEditor data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "toggles"}
      <Toggles data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "records"}
      <Records data={shown} {draft} readonly={!concept.writable || !!concept.locked} onEdit={changed} />
    {:else if concept.kind === "table"}
      <Table data={shown} />
    {:else if concept.kind === "series"}
      <Series data={shown} />
    {:else if concept.kind === "log"}
      <LogView data={shown} />
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
