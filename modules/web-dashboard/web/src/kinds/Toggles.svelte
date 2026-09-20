<script lang="ts">
  // 一组开关 / 选择器。每一项都带一句**为什么**（贡献者写的 `why`）——这是这套
  // 界面最省时间的一处：不用翻文档才知道这个开关是干什么的。
  import { untrack } from "svelte";

  //
  // 改动就地完成（下拉一选就是一次编辑），点保存才落盘。不弹模态、不默认收起：
  // 弹窗的意义是「打断」，而这里没有需要打断的决定。
  type Item = {
    id: string;
    label: string;
    kind: "select" | "bool" | "text";
    value: string | boolean;
    options?: string[];
    /** 空格子里的提示（「空 = 只听回环」这类）。空值格子靠它说清空着是什么意思。 */
    placeholder?: string;
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

  // 后端重读过（data 换了新对象）就清掉本地缓存，回落到 draft/props——否则这份
  // 旧值会在下一次保存时把**别处已经落盘的新值**盖回去（见 Records.svelte 里
  // 同一段注释：那边不清的后果是删除被重复提交）。
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
  });
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
        <!-- 开关就是开关：一个方框加个钩读起来像「表单项」，而这里表达的是
             「这个能力此刻开着没有」——一块能拨的轨道一眼就懂，也不用去数
             打钩和没打钩的区别。input 还是 checkbox：屏幕阅读器与键盘（空格）
             靠它，样式只负责把它画成开关。 -->
        <label class="sw">
          <input
            type="checkbox"
            role="switch"
            checked={values[it.id] === true}
            disabled={readonly}
            onchange={(e) => set(it.id, e.currentTarget.checked)}
          />
          <span class="track"><span class="knob"></span></span>
        </label>
      {:else if it.kind === "select"}
        <select
          value={String(values[it.id] ?? "")}
          disabled={readonly}
          onchange={(e) => set(it.id, e.currentTarget.value)}
        >
          <!-- 空值 = 「跟随缺省」这类语义，得有个看得见的名字：空 <option>
               在界面上就是一个没有任何字的选项，用户不知道它是什么。 -->
          {#each it.options ?? [] as o (o)}<option value={o}>{o || "—"}</option>{/each}
          {#if values[it.id] !== undefined && !(it.options ?? []).includes(String(values[it.id]))}
            <option value={String(values[it.id])}>{String(values[it.id])} (not in the list)</option>
          {/if}
        </select>
      {:else}
        <input
          class="mono"
          value={String(values[it.id] ?? "")}
          placeholder={it.placeholder}
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
  /* 开关：真 input 藏在轨道下面（键盘与读屏器照常），可见的是 .track/.knob。
     用 :focus-visible 给键盘用户一个焦点环——把 input 藏起来之后系统默认那个
     焦点框也一起没了，不补回来等于只有鼠标能用。 */
  .sw { display: inline-flex; cursor: pointer; }
  .sw input {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
    margin: 0;
  }
  .track {
    width: 34px;
    height: 18px;
    border-radius: 9px;
    background: var(--line);
    display: inline-block;
    position: relative;
    transition: background 0.15s;
  }
  .knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--ink);
    transition: transform 0.15s;
  }
  .sw input:checked + .track { background: var(--ok); }
  .sw input:checked + .track .knob { transform: translateX(16px); }
  .sw input:focus-visible + .track { outline: 2px solid var(--accent); outline-offset: 2px; }
  .sw input:disabled + .track { opacity: 0.5; cursor: not-allowed; }
</style>
