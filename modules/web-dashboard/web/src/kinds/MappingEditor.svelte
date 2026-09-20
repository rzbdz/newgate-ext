<script lang="ts">
  // 档位 ↔ 候选的绑定编辑器。**顺序就是 fallback 顺序**，所以每一行左边有上下
  // 移动——顺序是这里唯一的语义，不给它一个显眼的位置，用户会以为那是列表装饰。
  //
  // 产出的 edit 形状由贡献者定义（core/modules/config/view.go 的
  // applyProfileRoles）：`{"roles": {"<档位>": [{"provider","model"} | {"ref"}]}}`。
  // 关键一条：**整个 roles 一起交**，不是增量——后端是「替换 roles 这一个键」。
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
    roles: Role[];
    providers: Provider[];
  };
  type Roles = Record<string, Binding[]>;

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

  // 本地编辑（null = 还没动过）。**为什么不是「挂载时拷一份副本」**：副本只在
  // 挂载那一刻取一次值，之后 props 换了（用户在冲突里选了「用磁盘上那份」、或者
  // 点了 reload）它还是旧的——界面于是显示一份磁盘上已经不存在的档位表。
  //
  // 所以这里存的是「我改过没有」：没改过就跟着 props 走，改过就以本地为准。
  let edited = $state<Roles | null>(null);
  const roles = $derived(
    edited ?? (draft as { roles?: Roles })?.roles ?? toRoles(data.roles),
  );
  let newRole = $state("");

  function toRoles(list: Role[]): Roles {
    const out: Roles = {};
    for (const r of list ?? []) out[r.id] = (r.bindings ?? []).map((b) => ({ ...b }));
    return out;
  }

  // 每一次改动都：拷一份 → 在上面改 → 交出去。**不就地改 roles**：那可能是在改
  // props 里的对象（Svelte 的响应式看不见别人家的对象），改完界面不动，用户以为
  // 自己没点到。
  function push(mutate: (r: Roles) => void) {
    const next = JSON.parse(JSON.stringify(roles)) as Roles;
    mutate(next);
    edited = next;
    onEdit({ roles: JSON.parse(JSON.stringify(next)) });
  }

  function models(provider: string): string[] {
    return data.providers.find((p) => p.name === provider)?.models ?? [];
  }

  function addCandidate(id: string) {
    const first = data.providers[0];
    push((r) => r[id].push({ provider: first?.name ?? "", model: first?.models?.[0] ?? "" }));
  }
  function removeCandidate(id: string, i: number) {
    push((r) => r[id].splice(i, 1));
  }
  function move(id: string, i: number, by: number) {
    push((r) => {
      const list = r[id];
      const j = i + by;
      if (j < 0 || j >= list.length) return;
      [list[i], list[j]] = [list[j], list[i]];
    });
  }
  function addRole() {
    const id = newRole.trim();
    if (!id) return;
    push((r) => {
      if (!r[id]) r[id] = [];
    });
    newRole = "";
  }
  function removeRole(id: string) {
    push((r) => {
      delete r[id];
    });
  }
  // 引用与「provider+model」互斥（见 domain.Binding）：一条引用是「跟那一档走」，
  // 同时留着 provider 只会让解析路径出现两种解释。
  function setRef(id: string, i: number, ref: string) {
    if (ref) push((r) => { r[id][i] = { ref }; });
    else {
      const first = data.providers[0];
      push((r) => { r[id][i] = { provider: first?.name ?? "", model: first?.models?.[0] ?? "" }; });
    }
  }
</script>

<div class="head">
  <span class="mono dim">{data.file}</span>
  {#if data.default}<span class="pill">default</span>{/if}
  {#if data.pinned}<span class="pill">pinned</span>{/if}
  {#if data.excluded}<span class="pill">excluded</span>{/if}
  {#if data.extends}<span class="pill">extends {data.extends}</span>{/if}
  {#if data.description}<span class="dim">{data.description}</span>{/if}
</div>

{#each Object.keys(roles) as id (id)}
  <div class="role">
    <div class="row">
      <b class="mono">{id}</b>
      {#if !roles[id].length}<span class="dim">no candidates — this tier resolves to nothing</span>{/if}
      <span class="spacer"></span>
      {#if !readonly}
        <button class="tiny" onclick={() => addCandidate(id)}>+ candidate</button>
        <button class="tiny ghost" onclick={() => removeRole(id)} title="remove this tier from the file">remove tier</button>
      {/if}
    </div>
    {#each roles[id] as b, i (i)}
      <div class="row cand">
        <span class="idx mono dim">{i + 1}</span>
        {#if b.ref !== undefined}
          <input
            class="mono"
            value={b.ref}
            placeholder="another tier id"
            disabled={readonly}
            oninput={(e) => setRef(id, i, e.currentTarget.value)}
          />
        {:else}
          <select
            value={b.provider ?? ""}
            disabled={readonly}
            onchange={(e) => {
              const name = e.currentTarget.value;
              push((r) => {
                r[id][i].provider = name;
                r[id][i].model = models(name)[0] ?? r[id][i].model ?? "";
              });
            }}
          >
            <option value="">— provider —</option>
            {#each data.providers as p (p.name)}
              <option value={p.name}>{p.name}{p.has_key ? "" : " (no key)"}</option>
            {/each}
            {#if b.provider && !data.providers.some((p) => p.name === b.provider)}
              <option value={b.provider}>{b.provider} (not declared)</option>
            {/if}
          </select>
          <input
            class="mono model"
            list="models-{id}-{i}"
            value={b.model ?? ""}
            placeholder="model"
            disabled={readonly}
            oninput={(e) => {
              const m = e.currentTarget.value;
              push((r) => { r[id][i].model = m; });
            }}
          />
          <datalist id="models-{id}-{i}">
            {#each models(b.provider ?? "") as m (m)}<option value={m}></option>{/each}
          </datalist>
        {/if}
        <span class="spacer"></span>
        {#if !readonly}
          <label class="dim tiny">
            ref
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
      placeholder="add a tier (e.g. vision)"
      bind:value={newRole}
      onkeydown={(e) => e.key === "Enter" && addRole()}
    />
    <button class="tiny" onclick={addRole} disabled={!newRole.trim()}>add tier</button>
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
  label.tiny { display: flex; align-items: center; gap: 4px; font-size: 11px; }
</style>
