<script lang="ts">
  // 档位 ↔ 候选的绑定编辑器。**顺序就是 fallback 顺序**，所以每一行左边有上下
  // 移动——顺序是这里唯一的语义，不给它一个显眼的位置，用户会以为那是列表装饰。
  //
  // 产出的 edit 形状由贡献者定义（core/modules/config/view.go 的
  // applyProfileRoles）：`{"roles": {"<档位>": [{"provider","model"} | {"ref"}]}}`。
  // 关键一条：**整个 roles 一起交**，不是增量——后端是「替换 roles 这一个键」。
  import { untrack } from "svelte";
  import { t } from "../i18n";

  type Binding = { provider?: string; model?: string; ref?: string };
  type Role = { id: string; bindings: Binding[] };
  type Provider = {
    name: string;
    protocol?: string;
    base_url?: string;
    key_env?: string;
    has_key: boolean;
    models: string[];
  };
  type Data = {
    profile: string;
    file: string;
    base: string;
    description?: string;
    default: boolean;
    pinned?: boolean;
    excluded?: boolean;
    extends?: string;
    /** 可选的父档位（别的档位文件名，第一项是空串 = 不继承）。由后端给：界面
        不认识「有哪些档位文件」，它只知道这一格的下拉该列什么。 */
    extends_options?: string[];
    roles: Role[];
    providers: Provider[];
  };
  type Roles = Record<string, Binding[]>;

    // 这一张卡交出去的是**整份文件**：档位表 + 名字 + 继承。三者同属一份 .kv，分开
  // 成两张卡就会「你说你的、我说我的」（2026-09-20 之前正是那样：另一张「档位文件」
  // 卡管名字与继承，而它列的文件与这里的卡一一对应——用户的原话是「多余的啊」）。
  type Payload = { roles: Roles; name: string; extends: string };

  let {
    data,
    draft,
    readonly,
    onEdit,
    onDeleteFile,
  }: {
    data: Data;
    draft: unknown;
    readonly: boolean;
    onEdit: (v: unknown) => void;
    // 删除**立刻生效**（不走「改完再点保存」）：它动的是磁盘上的文件本身，让用户
    // 先点一次删除、再点一次保存，等于给一个不可撤销的动作配一道没用的仪式。
    // 它作用在**这一张卡自己**的文件上，所以不需要参数。
    //
    // 「新建」不在这里（见下面模板里那段注释）：它属于**这一节的工具条**。
    onDeleteFile?: () => void;
  } = $props();

  // 本地编辑（null = 还没动过）。**为什么不是「挂载时拷一份副本」**：副本只在
  // 挂载那一刻取一次值，之后 props 换了（用户在冲突里选了「用磁盘上那份」、或者
  // 点了 reload）它还是旧的——界面于是显示一份磁盘上已经不存在的档位表。
  //
  // 所以这里存的是「我改过没有」：没改过就跟着 props 走，改过就以本地为准。
  let edited = $state<Payload | null>(null);
  const cur = $derived<Payload>(
    edited ??
      (draft as Payload | undefined) ?? {
        roles: toRoles(data.roles),
        name: data.profile,
        extends: data.extends ?? "",
      },
  );
  const roles = $derived(cur.roles);
  let newRole = $state("");

  // 后端重读过（data 换了新对象）就清掉本地缓存，回落到 draft/props。
  //
  // 为什么必须清：这份缓存优先于 draft 与 props，而**一次成功保存之后 App 会全局
  // 重读**——不清的话，界面显示的仍是保存前那一份，下一次保存又把它写回去，把
  // 别处刚落盘的内容盖掉（Records.svelte 里同一段注释记着实测到的症状）。
  // 清掉不丢东西：没保存的改动住在 App 的 `drafts`（按概念 id 存），回落到它就是。
  let seen = untrack(() => data);
  $effect(() => {
    if (seen === data) return;
    seen = data;
    edited = null;
  });

  function toRoles(list: Role[]): Roles {
    const out: Roles = {};
    for (const r of list ?? []) out[r.id] = (r.bindings ?? []).map((b) => ({ ...b }));
    return out;
  }

  // 每一次改动都：拷一份 → 在上面改 → 交出去。**不就地改 cur**：那可能是在改
  // props 里的对象（Svelte 的响应式看不见别人家的对象），改完界面不动，用户以为
  // 自己没点到。
  function push(mutate: (p: Payload) => void) {
    const next = JSON.parse(JSON.stringify(cur)) as Payload;
    mutate(next);
    edited = next;
    onEdit(JSON.parse(JSON.stringify(next)));
  }

  function models(provider: string): string[] {
    return data.providers.find((p) => p.name === provider)?.models ?? [];
  }

  // 新候选的初值取**第一个 provider**（绝大多数情况下就是用户要的那家），但
  // **没有 provider 时留空**——`provider: ""` 写下去就是 `/模型名` 这种绑定，
  // 解析不出任何东西，而它在文件里看着还挺像样（2026-09-20 现场：一个 profile
  // 的档位变成了 `mid=/gemini-…`）。后端现在也拒这种绑定，这里是不让它发生。
  function addCandidate(id: string) {
    const first = data.providers[0];
    push((p) => {
      p.roles[id].push(
        first ? { provider: first.name, model: first.models?.[0] ?? "" } : {},
      );
    });
  }
  function removeCandidate(id: string, i: number) {
    push((p) => p.roles[id].splice(i, 1));
  }
  function move(id: string, i: number, by: number) {
    push((p) => {
      const list = p.roles[id];
      const j = i + by;
      if (j < 0 || j >= list.length) return;
      [list[i], list[j]] = [list[j], list[i]];
    });
  }
  function addRole() {
    const id = newRole.trim();
    if (!id) return;
    push((p) => {
      if (!p.roles[id]) p.roles[id] = [];
    });
    newRole = "";
  }
  function removeRole(id: string) {
    push((p) => {
      delete p.roles[id];
    });
  }
  // 引用与「provider+model」互斥（见 domain.Binding）：一条引用是「跟那一档走」，
  // 同时留着 provider 只会让解析路径出现两种解释。
  function setRef(id: string, i: number, ref: string) {
    if (ref) push((p) => { p.roles[id][i] = { ref }; });
    else {
      const first = data.providers[0];
      push((p) => {
        p.roles[id][i] = first
          ? { provider: first.name, model: first.models?.[0] ?? "" }
          : {};
      });
    }
  }
</script>

<div class="head">
  {#if !readonly}
    <!-- 名字与继承就在卡片头上：它们和下面的档位表同属**这一份文件**。改完点保存
         一起落盘（与档位表同一条：一次 CAS 写一份文件）。 -->
    <label class="meta">
      {t("name")}
      <input
        class="mono name"
        value={cur.name}
        disabled={readonly}
        oninput={(e) => push((p) => { p.name = e.currentTarget.value; })}
      />
    </label>
    <label class="meta">
      {t("extends")}
      <select
        value={cur.extends}
        disabled={readonly}
        onchange={(e) => push((p) => { p.extends = e.currentTarget.value; })}
      >
        {#each data.extends_options ?? [""] as o (o)}<option value={o}>{o || "—"}</option>{/each}
      </select>
    </label>
  {:else}
    <span class="mono dim">{data.profile}</span>
    {#if data.extends}<span class="pill">{t("extends {name}", { name: data.extends })}</span>{/if}
  {/if}
  {#if data.default}<span class="pill">{t("default")}</span>{/if}
  {#if data.pinned}<span class="pill">{t("pinned")}</span>{/if}
  {#if data.excluded}<span class="pill">{t("excluded")}</span>{/if}
  <span class="spacer"></span>
  <span class="mono dim file">{data.file}</span>
  <!-- 「再建一份」**不在这张卡上**：新建出来的那一份此刻还没有概念，没有哪张卡能
       挂它。它挂在**这一节的工具条**上（见 App.svelte 的 .secbar 与
       core/lib/view 的 Section.Actions），那儿永远够得着。 -->
  {#if !readonly && onDeleteFile}
    <button
      class="tiny ghost danger"
      onclick={() => {
        if (confirm(t("Delete profile “{name}” ({file})? This removes the file.", {
          name: cur.name, file: data.file,
        }))) onDeleteFile();
      }}
    >{t("delete this file")}</button>
  {/if}
</div>

{#each Object.keys(roles) as id (id)}
  <div class="role">
    <div class="row">
      <b class="mono">{id}</b>
      <!-- 空档位是「没写」：继承父 / 回落阶梯（2026-09-20 修复后它与没写等价）。
           不喊「解析不出任何东西」——那看着像硬错误，其实只是落回继承。 -->
      {#if !roles[id].length}<span class="dim">{t("no candidates — inherits or falls back")}</span>{/if}
      <span class="spacer"></span>
      {#if !readonly}
        <button class="tiny" onclick={() => addCandidate(id)}>{t("+ candidate")}</button>
        <button
          class="tiny ghost"
          onclick={() => removeRole(id)}
          title={t("remove this tier from the file")}
        >{t("remove tier")}</button>
      {/if}
    </div>
    {#each roles[id] as b, i (i)}
      <div class="row cand">
        <span class="idx mono dim">{i + 1}</span>
        {#if b.ref !== undefined}
          <input
            class="mono"
            value={b.ref}
            placeholder={t("another tier id")}
            disabled={readonly}
            oninput={(e) => setRef(id, i, e.currentTarget.value)}
          />
        {:else}
          <select
            value={b.provider ?? ""}
            disabled={readonly}
            onchange={(e) => {
              const prov = e.currentTarget.value;
              push((p) => {
                p.roles[id][i].provider = prov;
                p.roles[id][i].model = models(prov)[0] ?? p.roles[id][i].model ?? "";
              });
            }}
          >
            <option value="">{t("— provider —")}</option>
            {#each data.providers as p (p.name)}
              <option value={p.name}>{p.name}{p.has_key ? "" : t(" (no key)")}</option>
            {/each}
            {#if b.provider && !data.providers.some((p) => p.name === b.provider)}
              <option value={b.provider}>{b.provider}{t(" (not declared)")}</option>
            {/if}
          </select>
          <input
            class="mono model"
            list="models-{id}-{i}"
            value={b.model ?? ""}
            placeholder={t("model")}
            disabled={readonly}
            oninput={(e) => {
              const m = e.currentTarget.value;
              push((p) => { p.roles[id][i].model = m; });
            }}
          />
          <datalist id="models-{id}-{i}">
            {#each models(b.provider ?? "") as m (m)}<option value={m}></option>{/each}
          </datalist>
        {/if}
        <span class="spacer"></span>
        {#if !readonly}
          <label class="dim tiny">
            {t("ref")}
            <input
              type="checkbox"
              checked={b.ref !== undefined}
              onchange={(e) => setRef(id, i, e.currentTarget.checked ? id : "")}
            />
          </label>
          <button class="tiny ghost" onclick={() => move(id, i, -1)} disabled={i === 0}>↑</button>
          <button class="tiny ghost" onclick={() => move(id, i, 1)} disabled={i === roles[id].length - 1}>↓</button>
          <button class="tiny ghost" onclick={() => removeCandidate(id, i)}>×</button>
        {/if}
      </div>
    {/each}
  </div>
{/each}

{#if !readonly}
  <div class="row">
    <input
      placeholder={t("add a tier (e.g. vision)")}
      bind:value={newRole}
      onkeydown={(e) => e.key === "Enter" && addRole()}
    />
    <button class="tiny" onclick={addRole} disabled={!newRole.trim()}>{t("add tier")}</button>
  </div>
{/if}

<style>
  .head { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 10px; }
  .role { border-top: 1px dashed var(--line); padding: 8px 0; }
  .role:first-of-type { border-top: 0; }
  .cand { padding: 2px 0 2px 6px; }
  .cand select { min-width: 150px; }
  .cand .model { min-width: 220px; }
  .idx { width: 14px; }
  .danger { color: var(--danger, #c0392b); }
  label.tiny { display: flex; align-items: center; gap: 4px; font-size: 11px; }
  /* 名字与继承排在卡片最前面（它们是这一份文件的身份），文件路径退到右端当注脚。 */
  .meta { display: flex; align-items: center; gap: 6px; font-size: 11px; color: var(--dim); }
  .meta .name { min-width: 130px; }
  .file { font-size: 11px; }
</style>
