<script lang="ts">
  // 一组开关 / 选择器。每一项都带一句**为什么**（贡献者写的 `why`）——这是这套
  // 界面最省时间的一处：不用翻文档才知道这个开关是干什么的。
  //
  // 改动就地完成（下拉一选就是一次编辑），点保存才落盘。不弹模态、不默认收起：
  // 弹窗的意义是「打断」，而这里没有需要打断的决定。
  type Item = {
    id: string;
    label: string;
    kind: "select" | "bool" | "text";
    value: string | boolean;
    options?: string[];
    why?: string;
    /** 分组（模块名这类）。同一份列表里可以有好几组——见下面的分组小标题。 */
    group?: string;
    /** 危险级别（safe / quirk / footgun）。只影响显示：footgun 由贡献者放进
        另一张只读卡片，不会出现在这里。 */
    danger?: string;
  };
  type Data = { file: string; base: string; items: Item[] };

  let {
    data,
    draft,
    readonly,
    onEdit,
  }: {
    data: Data;
    draft: unknown;
    readonly: boolean;
    onEdit: (v: unknown) => void;
  } = $props();

  // 与 MappingEditor 同一条：存「我改过没有」，没改过就跟着 props 走——否则
  // reload 之后界面显示的还是上一份值（见那边的注释）。
  let edited = $state<Record<string, unknown> | null>(null);
  const values = $derived(
    edited ?? (draft as Record<string, unknown>) ?? fromItems(data.items),
  );

  function fromItems(items: Item[]): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const it of items ?? []) out[it.id] = it.value;
    return out;
  }

  // 交出**全部**项（不是只交改的那一项）：贡献者按 id 取自己认识的字段，多出来的
  // 键它不看，但少了的键它就当没写过——那会变成「改了一个开关，另一个被重置」。
  function set(id: string, v: unknown) {
    edited = { ...values, [id]: v };
    onEdit({ ...edited });
  }
</script>

<div class="row"><span class="mono dim">{data.file}</span></div>

{#each data.items ?? [] as it, i (it.id)}
  {#if it.group && it.group !== (data.items[i - 1]?.group)}
    <div class="group">{it.group}</div>
  {/if}
  <div class="item">
    <div class="row">
      <b>{it.label}</b>
      {#if it.danger && it.danger !== "safe"}
        <span class="pill" title="what turning this off costs">{it.danger}</span>
      {/if}
      <span class="spacer"></span>
      {#if it.kind === "bool"}
        <input
          type="checkbox"
          checked={values[it.id] === true}
          disabled={readonly}
          onchange={(e) => set(it.id, e.currentTarget.checked)}
        />
      {:else if it.kind === "select"}
        <select
          value={String(values[it.id] ?? "")}
          disabled={readonly}
          onchange={(e) => set(it.id, e.currentTarget.value)}
        >
          {#each it.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
          {#if values[it.id] !== undefined && !(it.options ?? []).includes(String(values[it.id]))}
            <option value={String(values[it.id])}>{String(values[it.id])} (not in the list)</option>
          {/if}
        </select>
      {:else}
        <input
          value={String(values[it.id] ?? "")}
          disabled={readonly}
          oninput={(e) => set(it.id, e.currentTarget.value)}
        />
      {/if}
    </div>
    {#if it.why}<div class="dim why">{it.why}</div>{/if}
  </div>
{/each}

<style>
  .group {
    margin: 12px 0 4px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--dim);
  }
  .group:first-child { margin-top: 4px; }
  .item { padding: 7px 0; border-top: 1px dashed var(--line); }
  .item:first-of-type { border-top: 0; }
  .why { font-size: 12px; margin-top: 2px; }
</style>
