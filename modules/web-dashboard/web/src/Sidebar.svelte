<script lang="ts">
  // 左边这一栏：**目录**。点一下切一节，不再往下滚。
  //
  // 它为什么存在：原来所有卡片按来源分组、一路往下铺（实测 4303px ≈ 4.8 屏），
  // 于是「改一个开关」这件四步就能做完的事，第一步是滚三屏。侧栏把这一步变成
  // 一次点击，而且是**位置固定**的——同一节永远在同一个地方，肌肉记忆有效。
  //
  // 它不认识任何模块：栏目名由各模块在登记时报（后端 `view.Sections`），这里只
  // 负责画。名字**不走 t()**：那是模块的内容，后端已经按当时的语言翻好了。
  import type { Section } from "./api";
  import { t } from "./i18n";

  let {
    sections,
    active,
    counts,
    onPick,
  }: {
    sections: Section[];
    active: string;
    counts: Map<string, { total: number; dirty: number; locked: number }>;
    onPick: (source: string) => void;
  } = $props();

  /**
   * 侧栏的行：几个分组标题 + 每一栏。
   *
   * **不归组的那几栏排在最前面**（今天只有「配置」，也是最大最常用的那一个）：
   * 分组标题的全部价值就是把它与下面那几类分开，而它自己在最上面时连标题都不用
   * 画——位置本身就是那句话。
   *
   * 分组标题**写在贡献者声明的那一组第一次出现的地方**，组内保持后端的来源序
   * （稳定：同一份装配跑两次，同一栏永远在同一个位置）。
   *
   * 与 TabStrip 的差别（那边「≥2 个成员才画标题」）是刻意的：那边的组是内核从
   * 档位名字**推**出来的（`claude-cheap` 归到 `claude`），一个人一族纯属噪音；
   * 这边是模块**自己说**「我属于哪一类」——说了就该看得见。而且这里若也按人数
   * 藏标题，一个只有一处声明的组会让那一栏**跳到最上面去**（它变成了「不归组」），
   * 位置跟着人数变，那比多一行小字糟得多。
   */
  type Row = { kind: "head"; name: string } | { kind: "sec"; s: Section };
  const rows = $derived.by<Row[]>(() => {
    const out: Row[] = [];
    for (const s of sections) if (!s.group) out.push({ kind: "sec", s });

    // 每一组**聚在一起**：先按组名第一次出现的次序定组的先后，再把整组成员一次
    // 列完。
    //
    // 不能「边走边插标题」——那一版是那么写的，而它是错的：成员按来源序来，于是
    // 一组的第二栏会被别组的标题拦在后面（实测：`gateway` 排在 `claudecode` 之后，
    // 结果「数据面」标题底下只有「熔断」，而「网关」跑到了 Clients 底下）。标题
    // 说的是「下面这几栏是一类」，底下就必须真的是那一类。
    const order: string[] = [];
    const byGroup = new Map<string, Section[]>();
    for (const s of sections) {
      if (!s.group) continue;
      const g = byGroup.get(s.group);
      if (g) g.push(s);
      else {
        byGroup.set(s.group, [s]);
        order.push(s.group);
      }
    }
    for (const g of order) {
      out.push({ kind: "head", name: g });
      for (const s of byGroup.get(g)!) out.push({ kind: "sec", s });
    }
    return out;
  });
</script>

<nav class="side">
  <p class="head">{t("sections")}</p>
  {#each rows as r, i (r.kind === "head" ? `h:${r.name}:${i}` : r.s.source)}
    {#if r.kind === "head"}
      <div class="group">{r.name}</div>
    {:else}
      {@const n = counts.get(r.s.source)}
      <!-- 整节锁灰：这一节**每一张卡都锁着**（今天就是「这家客户端没装」）。
           注意它只是**看起来**灰——仍然点得进去，因为详情照常要能看（用户的原话：
           「详情界面不可操作而已，但是为了展示我们的功能，我建议还是允许查看的」）。
           所以这里不给 disabled：那是「点不动」，而我们要的是「看了就知道动不了，
           想动得先装上」。 -->
      <button
        class="row"
        class:on={r.s.source === active}
        class:locked={!!n && n.total > 0 && n.locked === n.total}
        onclick={() => onPick(r.s.source)}
        title={r.s.source}
      >
        <span class="name">{r.s.title}</span>
        {#if n?.dirty}
          <span class="badge dirty" title={t("unsaved")}>{n.dirty}</span>
        {:else if n?.total}
          <span class="badge">{n.total}</span>
        {/if}
      </button>
    {/if}
  {/each}
  {#if !sections.length}
    <p class="dim pad">{t("nothing is contributing a view in this process.")}</p>
  {/if}
</nav>

<style>
  .side {
    overflow-y: auto;
    border-right: 1px solid var(--line);
    background: var(--panel);
    padding: 8px 0 12px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .head {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.7px;
    color: var(--dim);
    margin: 4px 12px 6px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: 0;
    padding: 6px 12px;
    color: var(--ink);
    cursor: pointer;
  }
  .row:hover { background: var(--panel-2); }
  /* 整节锁灰（见模板里那条注释）：灰是**陈述**，不是禁用手势——这一栏照样点得进去，
     只是告诉你「里面那些东西这台机器上用不上」。所以降不透明度、不降交互。 */
  .row.locked { opacity: 0.5; }
  .row.locked:hover { opacity: 0.75; }
  /* 分组标题：比栏名小一号、疏一点，读作「下面这几栏是一类」。**不是按钮**——
     点它没有意义（它代表的是一类，不是一栏），做成按钮只会多一个点不出东西的
     目标。 */
  .group {
    margin: 10px 12px 3px;
    font-size: 11px;
    letter-spacing: 0.6px;
    text-transform: uppercase;
    color: var(--dim);
  }
  /* 当前那一节：一条左侧竖线 + 底色。不用整块反白——侧栏是常驻的，抢眼会疲劳。 */
  .row.on {
    background: var(--panel-2);
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--dim);
  }
  /* 有没保存的改动时，徽标换成**改动数**并着色：那是一个会丢东西的状态，
     它得比「这张卡在这儿」显眼。 */
  .badge.dirty {
    color: var(--bg);
    background: var(--warn);
    border-radius: 999px;
    padding: 0 6px;
  }
  .pad { padding: 0 12px; }

  /* 窄屏：竖栏收成顶上一条横带（见 app.css 的 .shell 媒体查询，那边把第二行
     改成了 auto）。208px 的侧栏在 900px 以下会把 mapping-editor 挤到读不了。 */
  @media (max-width: 900px) {
    .side {
      flex-direction: row;
      align-items: center;
      overflow-x: auto;
      overflow-y: hidden;
      border-right: 0;
      border-bottom: 1px solid var(--line);
      padding: 4px 8px;
      white-space: nowrap;
    }
    .head { display: none; }
    .row { width: auto; }
    .row.on { box-shadow: inset 0 -2px 0 var(--accent); }
  }
</style>
