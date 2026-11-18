// The **single implementation** of fuzzy search: the model dropdown and any other long
// list in the future share one ruler.
//
// # Why not `includes`
//
// Model names are often assembled from several segments like
// `deepseek-v3.2-reasoner`, and what the user remembers is frequently just a middle
// segment (`reasoner`) or a few initials (`dsr`). Substring matching misses the latter,
// prefix matching misses the former. So this scores by **subsequence** match: characters
// must appear in order, but not necessarily adjacent; consecutive hits, word-boundary hits
// and earlier hits all score higher.
//
// # Why in a separate `.ts`
//
// It is a pure function, with no Svelte and no DOM. That way node can run it directly (to
// verify the algorithm on a dev machine), and future selectors can reuse it -- whereas one
// copy stuffed in a component and another copied into a test would sooner or later drift
// into two different "fuzzy"s.

/** fuzzyScore scores one candidate; -1 = no match. A higher score comes first. */
export function fuzzyScore(query: string, text: string): number {
  const q = query.trim().toLowerCase();
  const t = text.toLowerCase();
  if (!q) return 0;
  if (!t) return -1;

  // Exact / prefix / substring: these three tiers are the most intuitive, and they beat
  // subsequence scores outright.
  if (t === q) return 100000;
  if (t.startsWith(q)) return 90000 - t.length;
  const at = t.indexOf(q);
  if (at >= 0) return 80000 - at * 100 - t.length;

  // Subsequence: eat each character of q in order.
  let qi = 0;
  let score = 0;
  let last = -1;
  let run = 0;
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] !== q[qi]) continue;
    const boundary = ti === 0 || /[-_./:\s]/.test(t[ti - 1]);
    score += 10 + (boundary ? 18 : 0);
    if (ti === last + 1) {
      // Adjacent hits: `dsr` should not score the same in `deepseek-reasoner` as scattered
      // letters do.
      run++;
      score += run * 4;
    } else {
      run = 0;
    }
    last = ti;
    qi++;
  }
  if (qi < q.length) return -1;
  // Shorter candidates first: for the same hits, `glm-4` is more like what the user wants
  // than `glm-4-air-20250101`.
  return score - t.length;
}

/**
 * fuzzyFilter: every word separated by spaces must hit inside **any field**.
 *
 * Fields are separate, but a word may cross fields: typing `ds reasoner` hits
 * id=`deepseek-v3.2-reasoner` (both words are in the id), and also hits the combination
 * group=`deepseek` + name=`Reasoner`. That is steadier than "join the fields then match" --
 * the joins create substrings that do not exist.
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
