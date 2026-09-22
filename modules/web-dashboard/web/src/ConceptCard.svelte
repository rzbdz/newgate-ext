<script lang="ts">
  // 一张卡片 = 一个概念。它只做三件事：按 Kind 挑渲染器、显示「读不出来」的原因、
  // 标出「这张有没保存的改动」。
  //
  // 它**不认识任何模块**：`source` 只是个用来分组的字符串。加一个模块的界面意味着
  // 加一个 Kind 的渲染器（或者复用已有的），而不是在这张卡片里加一个 if。
  import type { Concept, ConceptAction, RowAction } from "./api";
  import { t } from "./i18n";
  import CodeEditor from "./kinds/CodeEditor.svelte";
  import MappingEditor from "./kinds/MappingEditor.svelte";
  import Records from "./kinds/Records.svelte";
  import Toggles from "./kinds/Toggles.svelte";
  import Series from "./kinds/Series.svelte";
  import LogView from "./kinds/LogView.svelte";
  import Table from "./kinds/Table.svelte";
  import Chains from "./kinds/Chains.svelte";

  let {
    concept,
    draft,
    preview,
    onEdit,
    onRevert,
    onDeleteFile,
    onAction,
    onRowAction,
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
    /**
     * 跑这张卡上的一个动作（见 api.ts 的 ConceptAction）。
     *
     * 与 `onDeleteFile` 那条同一个形状、同一条理由：动作作用在**这张卡**身上，
     * 所以界面不需要传「是哪张卡」——App 手里就有这张卡的 id。
     *
     * 也**不需要传它要改什么**：那件事住在贡献者那边（lib/view 的
     * Concept.Actions），界面只把点击转回去，然后重读一遍快照。
     */
    onAction?: (a: ConceptAction) => void;
    /**
     * 跑**表格里某一行**上的一个按钮（见 api.ts 的 runRowAction）。
     *
     * 比 `onAction` 多一层「哪一行」，理由与它多一层「哪张卡」是同一条：行是
     * **数据长出来的**（今天哪些 binding 在健康表里，取决于跑过哪些请求），
     * 所以「这一行能做什么」只有造出那一行的人知道，而界面得把行的身份带回去。
     *
     * 只有 table 这一种 Kind 会用到它——但它挂在卡片上而不是塞进渲染器的
     * props 里：卡片的职责就是「把 App 的手递给它选中的那个渲染器」。
     */
    onRowAction?: (row: string, a: RowAction) => void;
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
    <!-- 一句状态说明（见 lib/view 的 Concept.Note）。**它不是按钮**：没有 onclick，
         也不该长得像按钮——点了没有任何事发生，而一个点了没反应的按钮比没有按钮
         更让人困惑。
         它补的是一个信息缺口：档位卡上「已经在生效的那一份」不挂那对 apply 按钮
         （挂了是噪音），于是十六张卡里十五张有两个按钮、一张什么都没有，两张卡
         长得几乎一样，用户看不出哪一份在生效——而那恰恰是他打开这一节最想知道的
         事。「不画那个按钮」与「什么都不说」是两件事。 -->
    {#if concept.note}
      <span
        class="pill note"
        class:note-ok={concept.note.tone === "ok"}
        class:note-warn={concept.note.tone === "warn"}
        class:note-bad={concept.note.tone === "bad"}
        data-note={concept.note.tone ?? ""}
      >
        {concept.note.text}
      </span>
    {/if}
    <!-- 这张卡上的动作（「把这一份设为默认」这类）。**由贡献者注入**，界面只画
         按钮、把点击转回去——它不知道那个按钮会改什么，也不需要知道。
         锁死或读不出来时不画：那些动作多半也做不成，而一个点了没反应的按钮比
         没有按钮更让人困惑。 -->
    {#if !concept.locked && !concept.error}
      {#each concept.actions ?? [] as a (a.id)}
        <button class="tiny ghost" data-action={a.id} onclick={() => onAction?.(a)}>{a.label}</button>
      {/each}
    {/if}
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
      <Table data={shown} onAction={(row, a) => onRowAction?.(row, a)} />
    {:else if concept.kind === "chains"}
      <!-- 行上的按钮走 `onRowAction`（与 table 同一条），不是卡上的 `onAction`：
           它改的是**这张卡里的一行**，跑完卡还在，重读快照就对；而卡上的动作是给
           「把这一份设为默认」那种改完要换卡的事情用的。理由写在 Chains.svelte 里。 -->
      <Chains data={shown} onAction={(row, a) => onRowAction?.(row, a)} />
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
  /* 状态说明的语气（见 Concept.Note）：**只换颜色，不换形状**——它是陈述，不是
     按钮，也不该长得像警告条。`ok` 是「一切正常，这就是现在生效的那一份」，与健康
     表里那一格是同一个绿。边框跟着淡一点，免得这一格比卡片上真能点的东西还显眼。 */
  .note-ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .note-warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 45%, transparent); }
  .note-bad { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 45%, transparent); }
</style>
