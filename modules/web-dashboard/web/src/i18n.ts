// 界面骨架上的字（按钮、提示、空状态）。
//
// 为什么前端自己带一份目录，而不是找后端要：**概念标题已经是后端翻译好的**
// （profile 的名字、计数器分组名、开关点标题都从 i18n.T 出来），那些字属于模块。
// 而「保存」「重载」「没有任何概念」是这块界面的说法，属于界面自己——放进后端目录
// 等于让后端记住前端的按钮叫什么。
//
// 语言由**后端**给（快照里的 lang）：解析规则（NEWGATE_LANG > 配置 > LC_ALL > …）
// 只有一处实现，前端再猜一遍（navigator.language？）就有了第二处。两处不一致的
// 症状是中英混排，而那种错没人会报成 bug。
//
// 键就是那句英文（与 Go 侧同一条 gettext 传统）：加一句话不需要先想一个键名。
const en: Record<string, string> = {
  sections: "sections",
  "nothing is contributing a view in this process.":
    "nothing is contributing a view in this process.",
  "show the raw file": "show the raw file",
  "hide the raw file": "hide the raw file",
  "nothing in this section matches.": "nothing in this section matches.",
  remove: "remove",
  "new record": "new record",
  "nothing here yet": "nothing here yet",
  "+ add": "+ add",
  show: "show it",
  hide: "hide it",
  "{n} concepts": "{n} concepts",
  "filter — id, title, kind": "filter — id, title, kind",
  "as of {time}": "as of {time}",
  "auto-refresh": "auto-refresh",
  reload: "reload",
  save: "save",
  "no concepts — nothing installed in this process contributes a view.":
    "no concepts — nothing installed in this process contributes a view.",
  unsaved: "unsaved",
  revert: "revert",
  "read-only": "read-only",
  locked: "locked",
  "read-only — the controls for this file are in the other pane":
    "read-only — the controls for this file are in the other pane",
  "the contributor offers no way to write this one back (it may hold credentials)":
    "the contributor offers no way to write this one back (it may hold credentials)",
  "no renderer for kind “{kind}” yet — raw data:":
    "no renderer for kind “{kind}” yet — raw data:",
  "{path} changed on disk": "{path} changed on disk",
  "use theirs (drop my edit)": "use theirs (drop my edit)",
  "keep mine": "keep mine",
  "Something else wrote this file after this page loaded it (a CLI command, another browser tab, or a program of yours). Nothing has been written — pick one.":
    "Something else wrote this file after this page loaded it (a CLI command, another browser tab, or a program of yours). Nothing has been written — pick one.",
  yours: "yours",
  "on disk now": "on disk now",
  "no candidates — inherits or falls back": "no candidates — inherits or falls back",
  "+ candidate": "+ candidate",
  "remove tier": "remove tier",
  "remove this tier from the file": "remove this tier from the file",
  "delete this file": "delete this file",
  "name": "name",
  "extends": "extends",
  "Delete profile “{name}” ({file})? This removes the file.":
    "Delete profile “{name}” ({file})? This removes the file.",
  "Profile files": "Profile files",
  "another tier id": "another tier id",
  "— provider —": "— provider —",
  " (no key)": " (no key)",
  " (not declared)": " (not declared)",
  model: "model",
  ref: "ref",
  "add a tier (e.g. vision)": "add a tier (e.g. vision)",
  "add tier": "add tier",
  default: "default",
  pinned: "pinned",
  excluded: "excluded",
  "extends {name}": "extends {name}",
  redacted: "redacted",
  "credentials are replaced with *** before they leave the daemon":
    "credentials are replaced with *** before they leave the daemon",
  "total {n}": "total {n}",
  "counters are process-local — they reset when the daemon restarts":
    "counters are process-local — they reset when the daemon restarts",
  "no counters yet — nothing has gone through the gateway since it started.":
    "no counters yet — nothing has gone through the gateway since it started.",
  "no rows": "no rows",
  "no steps": "no candidates",
  "no profiles": "no profiles yet",
  tail: "tail",
  "only the tail is shown": "only the tail is shown",
  follow: "follow",
  "no log file yet — this daemon has not written anything.":
    "no log file yet — this daemon has not written anything.",
};

const zhHans: Record<string, string> = {
  sections: "目录",
  "nothing is contributing a view in this process.":
    "这个进程里没有模块贡献界面内容。",
  "show the raw file": "显示原文",
  "hide the raw file": "收起原文",
  "nothing in this section matches.": "这一节里没有匹配的东西。",
  remove: "删除",
  "new record": "新的一条",
  "nothing here yet": "还没有内容",
  "+ add": "+ 新增",
  show: "显示出来",
  hide: "遮起来",
  "{n} concepts": "{n} 个概念",
  "filter — id, title, kind": "过滤 —— id、标题、类型",
  "as of {time}": "数据时间 {time}",
  "auto-refresh": "自动刷新",
  reload: "重载",
  save: "保存",
  "no concepts — nothing installed in this process contributes a view.":
    "没有任何概念 —— 这个进程里装的模块都没有界面上要展示的东西。",
  unsaved: "未保存",
  revert: "撤销改动",
  "read-only": "只读",
  locked: "锁死",
  "read-only — the controls for this file are in the other pane":
    "只读 —— 这一栏是看那份文件的，改它用旁边那一栏控件",
  "the contributor offers no way to write this one back (it may hold credentials)":
    "贡献它的模块没有提供写回的方式（这份内容可能带凭据）",
  "no renderer for kind “{kind}” yet — raw data:":
    "还没有渲染 “{kind}” 这种类型 —— 原文如下：",
  "{path} changed on disk": "{path} 在磁盘上已经变了",
  "use theirs (drop my edit)": "用磁盘上那份（丢掉我的改动）",
  "keep mine": "保留我的",
  "Something else wrote this file after this page loaded it (a CLI command, another browser tab, or a program of yours). Nothing has been written — pick one.":
    "这个文件在页面加载之后被别人改过（一条命令行、另一个标签页、或者你自己的程序）。什么都还没写下去 —— 二选一。",
  yours: "你的",
  "on disk now": "磁盘上现在这份",
  "no candidates — inherits or falls back": "没有候选 —— 继承或回落",
  "+ candidate": "+ 候选",
  "remove tier": "删掉这一档",
  "remove this tier from the file": "把这一档从文件里删掉",
  "delete this file": "删除这个文件",
  "name": "名字",
  "extends": "继承自",
  "Delete profile “{name}” ({file})? This removes the file.":
    "删除档位“{name}”（{file}）？这会删掉这个文件。",
  "Profile files": "档位文件",
  "another tier id": "另一个档位名",
  "— provider —": "—— provider ——",
  " (no key)": "（没有 key）",
  " (not declared)": "（没有声明过）",
  model: "模型",
  ref: "引用",
  "add a tier (e.g. vision)": "加一个档位（比如 vision）",
  "add tier": "加档位",
  default: "默认",
  pinned: "固定",
  excluded: "排除",
  "extends {name}": "继承 {name}",
  redacted: "已脱敏",
  "credentials are replaced with *** before they leave the daemon":
    "凭据在离开守护进程之前就被换成了 ***",
  "total {n}": "合计 {n}",
  "counters are process-local — they reset when the daemon restarts":
    "计数器是进程内的 —— 守护进程重启就归零",
  "no counters yet — nothing has gone through the gateway since it started.":
    "还没有计数器 —— 网关起来之后没有请求经过。",
  "no rows": "没有数据",
  "no steps": "没有候选",
  "no profiles": "还没有任何档位文件",
  tail: "尾部",
  "only the tail is shown": "只显示最后一段",
  follow: "跟随",
  "no log file yet — this daemon has not written anything.":
    "还没有日志文件 —— 这个守护进程什么都还没写过。",
};

const dicts: Record<string, Record<string, string>> = { en, "zh-Hans": zhHans };

let current = en;

/** setLang 按后端解析出来的语言挑一份；不认识的标签退回英文。 */
export function setLang(tag: string): void {
  current = dicts[tag] ?? en;
}

/**
 * t 查一句话。`{name}` 形式的占位符按参数替换。
 *
 * 查不到时**原样返回英文那句**：界面照常可用，缺的那句在屏幕上就是英文——比空白
 * 或者一个键名好，也比静默回退到别的话好（那会让人以为界面就是那么写的）。
 */
export function t(msg: string, args?: Record<string, string | number>): string {
  const s = current[msg] ?? en[msg] ?? msg;
  if (!args) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in args ? String(args[k]) : m));
}
