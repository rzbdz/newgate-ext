/**
 * 演示页的**假数据**：一份完整的 `Snapshot`，就躺在旁边的 `snapshot.json` 里。
 *
 * # 为什么要抄真快照，而不是手写一份「差不多的」
 *
 * 站点上这一页嵌的是**真组件**（同一份 `../src/kinds/*`，同一个 `ConceptCard`），
 * 喂的却是假数据——所以数据的形状必须与后端给的**逐字一致**：Truth 一偏，屏幕上
 * 的表现是「这张卡画不出来」或者「少了半张」，而演示页不在 `web/dist` 里、
 * `web_test.go` 与 `i18n_test.go` 都看不见它，没有任何东西会红。
 *
 * 2026-09-23 之前这里是一大坨手写的 `data.ts`（约 1700 行），只铺开五节十张卡。
 * 那一版的问题不是写得不好，而是**它必然会漂**：手抄一份真快照，等于给自己留了
 * 一份没有守卫的副本。所以现在改成：从真快照（`GET /ui/api/snapshot`）摘下来、
 * 把私密的那几样换掉，直接入库当数据文件。改渲染器时这份数据不用动；真形状变了
 * （比如内核加了新的 Kind 字段），重跑一次下面那条命令就同步。
 *
 * # 摘录时换掉了什么（`tools/demo-snapshot.py` 里逐条写着）
 *
 *   - 本机真的上游地址（`api.rvcompute.com…` / `ark.cn-beijing.volces.com…`）
 *     → `api.example-gateway.com` 这类示例地址。**凭据本来就不在里面**：BFF 对
 *     `api_key` 那几格只回 `***`（见 `core/modules/config/view.go` 的 redact），
 *     摘下来之后又扫过一遍 `sk-…` / `Bearer` / `control_token`，一个都没有。
 *   - 磁盘路径 `/root/.config/newgate` → `~/.config/newgate`。
 *   - CAS 指纹（`base: sha256:…`）与上游的 call id：形状留着，值换掉。
 *   - 代理日志留最后 60 行（一整份 400 行的尾巴在演示页上看不完，也没人看）。
 *
 * 仍然在这份数据里的、且是**有意保留**的：模型名与 provider 名（`smt-deepseek`、
 * `gpt-5.6-sol`…）。它们是这个产品在演示什么的一部分——一屏摊开的候选链讲的就是
 * 「这个名字落到那家模型上」，把名字抹掉这张卡就没有内容了。那些名字不是凭据，
 * 也不是可用的地址。
 *
 * # 怎么重新摘一份
 *
 *     curl -s http://127.0.0.1:8899/ui/api/snapshot -o /tmp/snap.json
 *     python3 tools/demo-snapshot.py /tmp/snap.json \
 *       modules/web-dashboard/web/demo/snapshot.json
 *
 * 脚本只做上面那张替换表 + 取尾 60 行日志 + 固定 `generated_at`（它每跑一次都
 * 会变，而这份数据进了版本控制——不固定的话每次重摘都是一整份 diff）。
 */
import type { Snapshot } from "../src/api";
import { setLang } from "../src/i18n";
import raw from "./snapshot.json";

export const DEMO_LANG = "zh-Hans";

/** 那一份完整的快照。json 的形状由 api.ts 的 `Snapshot` 钉住（见下面的断言）。 */
export function snapshot(): Snapshot {
  return raw as unknown as Snapshot;
}

/** 让真组件按中文渲染（`t()` 查不到时回落英文，所以这一步不做的话界面上会中英混着）。 */
export function useDemoLanguage(): void {
  setLang(DEMO_LANG);
}
