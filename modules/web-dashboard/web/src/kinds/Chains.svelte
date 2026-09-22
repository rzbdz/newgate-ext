<script lang="ts">
  // 一屏摊开的候选链（形状见内核 lib/view 的 Chains）。
  //
  // 与 Table 的分工写在那边：表是「一行一个事实」，读的人只看；这里每一行**带着
  // 去处**（「这一档现在走这条链」），所以它比表多两样东西——链头单独占一格（用户
  // 唯一会问的问题是「为什么不是我想的那个」，眼睛就落在那里），以及行上的按钮。
  //
  // 三个约定，都是前端这边唯一的执行者：
  //
  //  1. **链头读 Head，不是 Steps[0]**。空链在前端自己去取第一个的话，「空链怎么
  //     办」这条判断就抄进了前端，而它本来就是后端算出来的结论（见 ChainRow.Head）。
  //  2. **一行 = 一档，顺序由贡献者给**。这里不排序、不分组、不认识档位名——
  //     `heavy`/`normal` 只是字符串，与模块名一样属于「后端翻好了的内容」。
  //  3. **tone 是语义，不是颜色**（与 Table 同一条）。认不出来的词一律当没给。
  type Step = { provider: string; model: string; profile?: string; note?: string; tone?: string };
  /** 行上的一个按钮。label 是贡献者写好的那句人话（后端已经翻过），界面不译。 */
  type Act = { id: string; label: string };
  type Role = {
    tier: string;
    label?: string;
    head?: string;
    steps?: Step[];
    note?: string;
    tone?: string;
    actions?: Act[];
  };
  type Card = { profile: string; file?: string; default?: boolean; roles: Role[] };
  type Data = { cards: Card[] };

  import { t } from "../i18n";
  import type { RowAction } from "../api";

  let {
    data,
    onAction,
  }: {
    data: Data;
    /**
     * 跑这一行上的一个按钮（见 api.ts 的 runRowAction）。
     *
     * **刻意走 `onRowAction` 这条路，而不是 `Concept.Actions`**：动作跑完之后界面
     * 只做一件事——重读快照。卡上的动作（`RunConceptAction`）是给「把这一份设为
     * 默认」那种**改完要换一张卡**的事情用的（档案卡自己会消失）；而这一行改的是
     * **同一张卡里的一行**，卡还在。两条路都丢掉了「跑一下」这个动作本身，只是回头
     * 看的对象不同——这正是它们该分开的地方。
     *
     * 行 ID 用 `profile + "\u0000" + tier`：见下面 rowID 那段（`\u0000` 在档位名里
     * 不出现，所以它是一道分不开的分隔符）。
     */
    onAction?: (row: string, a: RowAction) => void;
  } = $props();

  const cards = $derived(data?.cards ?? []);

  /**
   * 一行的身份：**这一行属于哪一份 profile 的哪一档**。
   *
   * 两边都要，因为「把 heavy 换成 zhipu/glm-4」这件事在 `demo.kv` 与 `alt.kv` 里
   * 是两件不同的事——只带档位名的话，后端没法知道该动哪一份文件（它只能去猜
   * 「当前生效的那一份」，而这一屏上明明每份都列着）。
   */
  function rowID(card: Card, role: Role): string {
    return `${card.profile}\u0000${role.tier}`;
  }

  /** 这一站来自哪一份 profile——**与卡片同名时不重复写**（那是绝大多数情况）。 */
  function foreign(card: Card, s: Step): boolean {
    return !!s.profile && s.profile !== card.profile;
  }

  function toneClass(tone: string | undefined): string {
    return tone === "ok" || tone === "warn" || tone === "bad" ? `t-${tone}` : "";
  }
</script>

{#each cards as card (card.profile)}
  <div class="chain">
    <!-- 卡头：这份 profile 的名字 + 「它此刻是不是生效的那一份」。
         后者与 Concept.Note 那条「当前配置」是同一个意思、同一份语气（绿），
         因为用户在这一屏上要找的第一件事就是「我现在到底在走谁」。 -->
    <div class="row head">
      <span class="profile">{card.profile}</span>
      {#if card.default}
        <span class="pill note-ok" data-default="1">{t("default")}</span>
      {/if}
      {#if card.file}
        <span class="meta">{card.file}</span>
      {/if}
    </div>

    <ul class="roles">
      {#each card.roles as role (role.tier)}
        <li>
          <div class="row tier">
            <span class="tier-name" title={role.tier}>{role.label ?? role.tier}</span>
            <!-- 链头单独一格（见上面第 1 条）。没有任何候选时它是一句陈述，
                 不是一个空着的格子——空着的话读的人会以为这条链没加载出来。 -->
            {#if role.head}
              <span class="head-name {toneClass(role.tone)}">{role.head}</span>
            {:else}
              <span class="dim">{t("no steps")}</span>
            {/if}
            <span class="spacer"></span>
            {#each role.actions ?? [] as a (a.id)}
              <!-- 字由贡献者给（后端翻好），所以这里不套 t()。 -->
              <button
                class="tiny ghost"
                data-action={a.id}
                title={a.label}
                onclick={() => onAction?.(rowID(card, role), a)}
              >
                {a.label}
              </button>
            {/each}
          </div>

          <!-- 链的展开：链头之内还有谁。**只有一站时不画**——那一行会与上面那格
               一模一样，多一层缩进只是把同一个名字说两遍。 -->
          {#if (role.steps?.length ?? 0) > 1}
            <ol class="steps">
              {#each role.steps ?? [] as s, i (i)}
                <li class={toneClass(s.tone)}>
                  <span class="idx">{i + 1}</span>
                  <!-- provider 与 model 分开排版（中间那根斜杠是分隔符，不是数据
                       的一部分）：一行 `ark/deepseek-v3` 读起来像一个名字，而它们
                       是两个可以分别去查的东西。 -->
                  <span class="binding">
                    <span class="prov">{s.provider}</span><span class="slash">/</span
                    ><span class="model">{s.model}</span>
                  </span>
                  {#if foreign(card, s)}
                    <!-- 跨 profile 的那几站**必须写出处**：不写的话用户会去自己
                         正看着的这份文件里找一个不存在的候选。 -->
                    <span class="pill from">{s.profile}</span>
                  {/if}
                  {#if s.note}
                    <span class="dim why">{s.note}</span>
                  {/if}
                </li>
              {/each}
            </ol>
          {/if}

          <!-- 链尾那句总结（今天就是「跳过 N 个候选」）。它在**行的下面**而不是
               行内，因为它说的是被跳过的那些候选——那些一步都不在链上，行内没有
               它们的位置。 -->
          {#if role.note}
            <p class="dim skip">{role.note}</p>
          {/if}
        </li>
      {/each}
    </ul>
  </div>
{/each}

{#if !cards.length}
  <!-- 空与「坏了」要长得不一样：这里说的是「这台机器上一份 profile 都没有」，
       而读不出来（文件坏了之类）由卡片自己的 error 分支说。 -->
  <p class="dim">{t("no profiles")}</p>
{/if}

<style>
  /* 一叠卡之间用一条线分开，不用边框：这一整块本来就是**一张卡片的内容**
     （外面那层 .card 已经有了边框），里面再画一圈框就是框套框。 */
  .chain + .chain {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid var(--line);
  }
  .head { align-items: baseline; gap: 8px; margin-bottom: 6px; }
  .profile { font-weight: 600; font-size: 13px; }
  .meta { color: var(--dim); font-family: var(--mono); font-size: 11px; }

  .roles { list-style: none; margin: 0; padding: 0; }
  .roles > li + li { margin-top: 7px; }
  .tier { align-items: baseline; gap: 8px; }
  /* 档位名占固定宽度：不固定的话每行的箭头会参差不齐，而这几行的价值恰恰在于
     **竖着比**（heavy 走谁、normal 走谁）。 */
  .tier-name { color: var(--dim); font-size: 12px; min-width: 62px; }
  .head-name { font-family: var(--mono); font-size: 12px; }

  .steps {
    list-style: none;
    margin: 4px 0 0;
    padding: 0 0 0 62px; /* 与上面的链头对齐：缩进量 = tier-name 的宽度 */
  }
  .steps > li {
    display: flex;
    align-items: baseline;
    gap: 6px;
    font-size: 11px;
    color: var(--dim);
    padding: 1px 0;
  }
  .idx {
    font-family: var(--mono);
    min-width: 12px;
    opacity: 0.5;
  }
  .binding { font-family: var(--mono); color: var(--ink); opacity: 0.75; }
  .slash { opacity: 0.45; }
  .from { font-size: 10px; padding: 0 5px; }
  .why { font-size: 11px; }
  .skip { font-size: 11px; margin: 3px 0 0 62px; }

  /* 状态说明那套语气（与 ConceptCard 的 note 同一份）：**只换颜色，不换形状**。 */
  .note-ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 45%, transparent); }
  .t-ok { color: var(--ok); }
  .t-warn { color: var(--warn); }
  .t-bad { color: var(--danger); }
</style>
