// 模糊搜索的**唯一实现**：模型下拉与将来别的长列表共用一把尺子。
//
// # 为什么不是 `includes`
//
// 模型名常常是 `deepseek-v3.2-reasoner` 这种由几段拼出来的东西，而用户记得的
// 往往只是中间那一段（`reasoner`）或者几个首字母（`dsr`）。子串匹配漏掉后一种，
// 前缀匹配漏掉前一种。所以这里按**子序列**匹配打分：字符必须按顺序出现，但不
// 要求相邻；连续命中、词边界命中、越早命中都加分。
//
// # 为什么放在一个独立的 `.ts` 里
//
// 它是纯函数，没有 Svelte、没有 DOM。这样它能被 node 直接跑（开发机上验算法），
// 也能被将来别的选择器复用——而组件里塞一份、测试里抄一份的写法，两份迟早会
// 漂成两种「模糊」。

/** fuzzyScore 给一个候选打分；-1 = 不匹配。分数越大越靠前。 */
export function fuzzyScore(query: string, text: string): number {
  const q = query.trim().toLowerCase();
  const t = text.toLowerCase();
  if (!q) return 0;
  if (!t) return -1;

  // 完全相等 / 前缀 / 子串：这三档最符合直觉，直接压过子序列的分数。
  if (t === q) return 100000;
  if (t.startsWith(q)) return 90000 - t.length;
  const at = t.indexOf(q);
  if (at >= 0) return 80000 - at * 100 - t.length;

  // 子序列：按顺序吃到 q 的每一个字符。
  let qi = 0;
  let score = 0;
  let last = -1;
  let run = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] !== q[qi]) continue;
    const boundary = ti === 0 || /[-_./:\s]/.test(t[ti - 1]);
    score += 10 + (boundary ? 18 : 0);
    if (ti === last + 1) {
      // 相邻命中：`dsr` 在 `deepseek-reasoner` 里不该与散落的字母同分。
      run++;
      score += run * 4;
    } else {
      run = 0;
    }
    last = ti;
    qi++;
  }
  if (qi < q.length) return -1;
  // 短候选优先：同样的命中，`glm-4` 比 `glm-4-air-20250101` 更像用户要找的。
  return score - t.length;
}

/**
 * fuzzyFilter 用空格分开的每一个词都必须在**任意字段**里命中。
 *
 * 字段分开、但词可以跨字段：输入 `ds reasoner` 能命中 id=`deepseek-v3.2-reasoner`
 * （两个词都在 id 里），也能命中 group=`deepseek` + name=`Reasoner` 的组合。
 * 这比「把字段拼起来再匹配」更稳——拼接处会造出并不存在的子串。
 */
export function fuzzyFilter<T>(
  query: string,
  items: readonly T[],
  fields: (item: T) => (string | undefined)[],
): T[] {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [...items];

  const scored: { item: T; score: number; order: number }[] = [];
  for (let order = 0; order < items.length; order++) {
    const item = items[order];
    const hs = fields(item).filter((s): s is string => !!s);
    let total = 0;
    let ok = true;
    for (const w of words) {
      let best = -1;
      for (const h of hs) best = Math.max(best, fuzzyScore(w, h));
      if (best < 0) {
        ok = false;
        break;
      }
      total += best;
    }
    if (ok) scored.push({ item, score: total, order });
  }
  scored.sort((a, b) => b.score - a.score || a.order - b.order);
  return scored.map((s) => s.item);
}
