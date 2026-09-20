<script lang="ts">
  // 一组记录：每条几个字段，字段自己声明类型（text / select / secret / lines）。
  //
  // 与 Toggles 的分工：那个是「一组同构的开关」，每条一个值；这里每条是一张**小
  // 表单**，条数还会增减。两者都要，因为把 provider 那种形状塞进 toggles 的结果
  // 是两边都别扭。
  //
  // 改动就地完成、点保存才落盘（与这套界面的别处一致）。交出去的是**全部记录**：
  // 没交的就是删掉了——所以删除不需要第二个接口（见 config 的 applyProviders）。
  import { untrack } from "svelte";
  import { t } from "../i18n";

  type Kind = "text" | "select" | "secret" | "lines";
  type Field = {
    id: string;
    label: string;
    kind: Kind;
    value?: string;
    options?: string[];
    placeholder?: string;
    why?: string;
  };
  // 名字不叫 Record：那是 TypeScript 自带的泛型，撞上之后报错报在别处，
  // 排查起来像见鬼。base 是这一条代表的那份文件加载时的基线（可写记录集才有，
  // 删它时要 CAS 判断，见 view.Record.Base）。
  type Rec = { id: string; label: string; fields: Field[]; removable?: boolean; base?: string };
  type Data = { file?: string; base?: string; items: Rec[]; can_add?: boolean; add_label?: string };

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

  // key 是**渲染身份**，id 是**后端身份**。两者分开，是因为「同一行」这件事在
  // 整段生命周期里必须稳定，而 id 有一段时间是空的（新加的行还没保存，后端此刻
  // 还不知道它）。拿数组下标当身份是这张卡之前的做法，代价实测过：删掉一行之后
  // Svelte 按 key 复用 DOM，而 spec（取自 data.items[i]）与 values（取自 items[i]）
  // 来自两个不同的数组，一旦长度不一致就错位——最刺眼的症状是**卡片标题还写着
  // 已删掉的那个文件名，底下的字段却是下一个文件的**。
  type Item = { key: string; id: string; base?: string; values: globalThis.Record<string, string> };

  // 与 MappingEditor/Toggles 同一条：存「我改过没有」，没改过就跟着 props 走——
  // 否则后台刷新之后界面显示的还是一份旧值。
  //
  // 交出去的形状是 `{"items": [...], "removed": [...]}`，不是光秃秃一个数组：
  // `edit` 这坨 JSON 是**概念自己定的**，而它迟早要能装下第二种动作（「这一条
  // 要删」之类）。`removed` 显式报删掉的 id+base——不靠「少交一条被推断成删」，
  // 因为那份记录被删的代价可能是删一个文件（profiles 卡），推断出来太危险。
  // 已知只读记录集（providers）无视 removed，也不要求 base（apply 时按 id 找）。
  let edited = $state<Item[] | null>(null);
  let removedAccum = $state<{ id: string; base?: string }[]>([]);
  const items = $derived(
    edited ?? (draft as { items?: Item[] } | undefined)?.items ?? fromData(data),
  );
  /** 初始化时把后端快照里已经被删的也带回来（按 base 取），刷新之间不会丢它们。 */
  const removed = $derived(
    removedAccum.length
      ? removedAccum
      : (draft as { removed?: { id: string; base?: string }[] } | undefined)?.removed ?? [],
  );

  /** 眼睛：把某一格的密码型输入变成明文。**只为「我刚粘了什么」**——值本身
      （已存在的那一份）根本不在这边，所以点亮它不会reveal 出任何东西来。 */
  let shown = $state<Record<string, boolean>>({});

  /** 新行的渲染身份：一个进程内单调的计数器就够（它不必跨刷新稳定——新行本来
      就没保存过，刷新之后它该消失）。 */
  let seq = 0;

  function fromData(d: Data): Item[] {
    return (d.items ?? []).map((r) => ({
      key: r.id,
      id: r.id,
      base: r.base,
      values: Object.fromEntries((r.fields ?? []).map((f) => [f.id, f.value ?? ""])),
    }));
  }

  /**
   * 新行的字段形状 = **已有第一条的形状**（同一种记录集里每条字段一样）。
   * 之前新行没有字段可画：spec 取自 `data.items[i]`，而新行的下标已经越过了那个
   * 数组——于是「新增」出来一张只有标题、没有一个输入框的空卡片，等于新增不了。
   * 这里不带 value（新行从空开始），Options 原样沿用（比如档位的「继承自」下拉
   * 该列出别的档位）。
   */
  function blankFields(): Field[] {
    return (data.items?.[0]?.fields ?? []).map((f) => ({ ...f, value: "" }));
  }

  /**
   * 后端**重读过**（`data` 换了一个新对象）就把本地这份缓存清掉。
   *
   * 不清的代价实测过：删掉一行 → 保存成功 → App 全局重读 → `data` 是新的，可
   * `removedAccum` 里还留着那条 `(id, base)`。于是**下一次任何一次保存**都会把它
   * 再交一遍，而后端一看那份文件已经不在盘上了 → StaleError「别人改过，重载后再
   * 试」。用户看到的是「我删 extra，它报 claude-cheap.kv」——一条与自己刚才动作
   * 无关的报错。
   *
   * 清掉不会丢东西：**没保存的改动住在 App 的 `drafts` 里**（按概念 id 存），
   * `items`/`removed` 的派生链会立刻回落到它，屏幕上一点变化都没有。
   */
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
    removedAccum = [];
    shown = {};
  });

  function specOf(it: Item): Rec {
    const found = data.items.find((r) => r.id === it.id);
    // 新行（id 为空）后端此刻还不知道它，用同类第一条的字段形状补一张空白表单。
    if (found) return found;
    return { id: "", label: "", fields: blankFields(), removable: false };
  }

  function push(next: Item[]) {
    edited = next;
    onEdit({ items: next, removed });
  }

  function setValue(key: string, field: string, v: string) {
    push(items.map((it) => (it.key === key ? { ...it, values: { ...it.values, [field]: v } } : it)));
  }

  function remove(key: string) {
    const it = items.find((x) => x.key === key);
    // 把删掉这条的 (id, base) 攒住——后端用它做 CAS 删，盘上别人刚改的不会被删。
    if (it?.id) removedAccum = [...removedAccum, { id: it.id, base: it.base }];
    push(items.filter((x) => x.key !== key));
  }

  function add() {
    // 新记录没有 id（贡献者据此认「这是新增的」），字段全空——用户从零填一张。
    push([...items, { key: "new:" + ++seq, id: "", values: {} }]);
  }
</script>

{#if data.file}<div class="row"><span class="mono dim">{data.file}</span></div>{/if}

{#each items as it (it.key)}
  {@const spec = specOf(it)}
  <div class="rec">
    <div class="row head">
      <b>{spec.label || it.values.name || t("new record")}</b>
      <span class="spacer"></span>
      {#if !readonly && spec.removable}
        <button class="tiny ghost" onclick={() => remove(it.key)}>{t("remove")}</button>
      {/if}
    </div>
    {#each spec.fields as f (f.id)}
      <div class="field">
        <label for="f-{it.key}-{f.id}">{f.label}</label>
        {#if f.kind === "select"}
          <select
            id="f-{it.key}-{f.id}"
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            onchange={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          >
            {#each f.options ?? [] as o (o)}<option value={o}>{o || "—"}</option>{/each}
          </select>
        {:else if f.kind === "lines"}
          <textarea
            id="f-{it.key}-{f.id}"
            rows="3"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          ></textarea>
        {:else if f.kind === "secret"}
          <!-- 值不在这一侧：这一格来时是空的，敲了才是改（见 lib/view 的 FieldSecret）。
               眼睛只对「我刚敲/刚粘的那一串」有用——那正是最常见的核对动作。 -->
          <div class="row">
            <input
              id="f-{it.key}-{f.id}"
              class="mono grow"
              type={shown[it.key + "#" + f.id] ? "text" : "password"}
              autocomplete="off"
              placeholder={f.placeholder}
              value={it.values[f.id] ?? ""}
              disabled={readonly}
              oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
            />
            <button
              class="tiny ghost"
              title={shown[it.key + "#" + f.id] ? t("hide") : t("show")}
              onclick={() => (shown[it.key + "#" + f.id] = !shown[it.key + "#" + f.id])}
            >
              {shown[it.key + "#" + f.id] ? "🙈" : "👁"}
            </button>
          </div>
        {:else}
          <input
            id="f-{it.key}-{f.id}"
            class="mono"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(it.key, f.id, e.currentTarget.value)}
          />
        {/if}
        {#if f.why}<div class="dim why">{f.why}</div>{/if}
      </div>
    {/each}
  </div>
{/each}

{#if !items.length}
  <p class="dim">{t("nothing here yet")}</p>
{/if}

{#if !readonly && data.can_add}
  <button onclick={add}>{data.add_label || t("+ add")}</button>
{/if}

<style>
  .rec {
    border: 1px solid var(--line);
    border-radius: var(--radius);
    padding: 10px 12px;
    margin-bottom: 10px;
  }
  .head { margin-bottom: 6px; }
  .field {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr);
    gap: 6px 10px;
    align-items: center;
    padding: 3px 0;
  }
  .field label { color: var(--dim); }
  .field input, .field select, .field textarea { width: 100%; }
  .field textarea { font-family: var(--mono); font-size: 12px; resize: vertical; }
  .why { grid-column: 2; font-size: 12px; }
  .grow { flex: 1; }
  @media (max-width: 700px) {
    .field { grid-template-columns: minmax(0, 1fr); }
    .why { grid-column: 1; }
  }
</style>
