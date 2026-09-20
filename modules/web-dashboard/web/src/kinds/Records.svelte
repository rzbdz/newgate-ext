<script lang="ts">
  // 一组记录：每条几个字段，字段自己声明类型（text / select / secret / lines）。
  //
  // 与 Toggles 的分工：那个是「一组同构的开关」，每条一个值；这里每条是一张**小
  // 表单**，条数还会增减。两者都要，因为把 provider 那种形状塞进 toggles 的结果
  // 是两边都别扭。
  //
  // 改动就地完成、点保存才落盘（与这套界面的别处一致）。交出去的是**全部记录**：
  // 没交的就是删掉了——所以删除不需要第二个接口（见 config 的 applyProviders）。
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
  // 排查起来像见鬼。
  type Rec = { id: string; label: string; fields: Field[]; removable?: boolean };
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

  type Item = { id: string; values: globalThis.Record<string, string> };

  // 与 MappingEditor/Toggles 同一条：存「我改过没有」，没改过就跟着 props 走——
  // 否则后台刷新之后界面显示的还是一份旧值。
  //
  // 交出去的形状是 `{"items": [...]}`，不是光秃秃一个数组：`edit` 这坨 JSON 是
  // **概念自己定的**，而它迟早要能装下第二种动作（「这一条要删」之类）。数组的
  // 外层留一个对象，将来加字段不必改协议——`applyProviders` 读的也是 `.items`。
  let edited = $state<Item[] | null>(null);
  const items = $derived(edited ?? (draft as { items?: Item[] } | undefined)?.items ?? fromData(data));

  /** 眼睛：把某一格的密码型输入变成明文。**只为「我刚粘了什么」**——值本身
      （已存在的那一份）根本不在这边，所以点亮它不会reveal 出任何东西来。 */
  let shown = $state<Record<string, boolean>>({});

  function fromData(d: Data): Item[] {
    return (d.items ?? []).map((r) => ({
      id: r.id,
      values: Object.fromEntries((r.fields ?? []).map((f) => [f.id, f.value ?? ""])),
    }));
  }

  function push(next: Item[]) {
    edited = next;
    onEdit({ items: next });
  }

  function setValue(i: number, field: string, v: string) {
    push(items.map((it, j) => (j === i ? { ...it, values: { ...it.values, [field]: v } } : it)));
  }

  function remove(i: number) {
    push(items.filter((_, j) => j !== i));
  }

  function add() {
    // 新记录没有 id（贡献者据此认「这是新增的」），字段全空——用户从零填一张。
    push([...items, { id: "", values: {} }]);
  }
</script>

{#if data.file}<div class="row"><span class="mono dim">{data.file}</span></div>{/if}

{#each items as it, i (i)}
  {@const spec = data.items[i]}
  <div class="rec">
    <div class="row head">
      <b>{spec?.label || it.values.name || t("new record")}</b>
      <span class="spacer"></span>
      {#if !readonly && spec?.removable}
        <button class="tiny ghost" onclick={() => remove(i)}>{t("remove")}</button>
      {/if}
    </div>
    {#each spec?.fields ?? [] as f (f.id)}
      <div class="field">
        <label for="f-{i}-{f.id}">{f.label}</label>
        {#if f.kind === "select"}
          <select
            id="f-{i}-{f.id}"
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            onchange={(e) => setValue(i, f.id, e.currentTarget.value)}
          >
            {#each f.options ?? [] as o (o)}<option value={o}>{o || "—"}</option>{/each}
          </select>
        {:else if f.kind === "lines"}
          <textarea
            id="f-{i}-{f.id}"
            rows="3"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(i, f.id, e.currentTarget.value)}
          ></textarea>
        {:else if f.kind === "secret"}
          <!-- 值不在这一侧：这一格来时是空的，敲了才是改（见 lib/view 的 FieldSecret）。
               眼睛只对「我刚敲/刚粘的那一串」有用——那正是最常见的核对动作。 -->
          <div class="row">
            <input
              id="f-{i}-{f.id}"
              class="mono grow"
              type={shown[i + "#" + f.id] ? "text" : "password"}
              autocomplete="off"
              placeholder={f.placeholder}
              value={it.values[f.id] ?? ""}
              disabled={readonly}
              oninput={(e) => setValue(i, f.id, e.currentTarget.value)}
            />
            <button
              class="tiny ghost"
              title={shown[i + "#" + f.id] ? t("hide") : t("show")}
              onclick={() => (shown[i + "#" + f.id] = !shown[i + "#" + f.id])}
            >
              {shown[i + "#" + f.id] ? "🙈" : "👁"}
            </button>
          </div>
        {:else}
          <input
            id="f-{i}-{f.id}"
            class="mono"
            placeholder={f.placeholder}
            value={it.values[f.id] ?? ""}
            disabled={readonly}
            oninput={(e) => setValue(i, f.id, e.currentTarget.value)}
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
