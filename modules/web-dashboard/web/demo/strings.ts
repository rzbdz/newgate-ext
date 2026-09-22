/**
 * 演示页**自己的**那几句话。
 *
 * # 为什么不放进 `src/i18n.ts`
 *
 * 那份字典是**产品的**，而它有一条棘轮守着：`TestShippedBundleHasEverySentence`
 * 要求字典里的每一句都出现在**发出去的那个 bundle**（`web/dist`）里。演示页的产物
 * 落在 `site/dist/demo`、永不进 `web/dist`，所以往产品字典里加一句演示文案，红的是
 * 那条测试——而且报的错（「这句话在源字典里，却不在发出去的 bundle 里」）会把下一个
 * 人指向「是不是忘了 pnpm build」，与真实原因（那是演示页的话）毫无关系。
 *
 * 所以这边自立一份，键仍然是**那句英文**（与产品同一条 msgid 传统），
 * 加语言 = 加一列。
 *
 * # 为什么现在只有中文
 *
 * 站点今天是中文优先（见 `site/src/site.json`：English 还是未写状态）。演示页在
 * 站点里，所以它的这几句话也跟着站点的语言走。英文那列**留在这里**——不是忘了
 * 写，而是写了也没人会看见（站点上没有英文页可以跳过来）。
 *
 * 其余那些界面骨架上的字（保存、撤销、删除、目录、还没改过…）**不在这里**：它们是
 * 产品的词汇，`t()` 已经有了。两处重复写一遍的结果是它们会漂成两种说法。
 */

const zhHans: Record<string, string> = {
  "this page is the real interface with made-up data — nothing you click leaves the browser":
    "这一页是真界面配假数据 —— 你点的每一下都不会离开浏览器",
  "this is a demo — “{action}” would go to the real daemon; nothing is written here":
    "这是演示 —— “{action}”真的会去问守护进程，这里什么都没写",
  "this is a demo — “{action}” on “{row}” would talk to the real daemon":
    "这是演示 —— “{action}”（第 {row} 行）真的会去问守护进程",
  "this is a demo — save would write these files; here it writes nothing":
    "这是演示 —— 保存真的会把这几份文件写下去，这里什么都没写",
  "this is a demo — no file would be deleted": "这是演示 —— 不会真的删掉任何文件",
};

/** d 查一句演示页自己的话。与产品的 `t()` 同一条语义：查不到时原样返回英文那句。 */
export function d(msg: string, args?: Record<string, string | number>): string {
  const s = zhHans[msg] ?? msg;
  if (!args) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in args ? String(args[k]) : m));
}
