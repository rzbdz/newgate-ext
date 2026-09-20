// 浏览器里跑的那一层检查：**界面真的渲染出来了吗、点了真的落盘吗**。
//
// # 为什么需要第五层
//
// Go 那几层（单测 / BFF 的 e2e）验的是**契约**：端点回什么、状态码对不对。它们
// 看不见「界面把两个复选框放在一起了」「同一屏上两种语言」「点保存没反应」这类
// 事——2026-09-20 真去渲染了一次，一次就抓到三个，而当时**没有一条测试会红**。
//
// # 为什么不在 CI 里
//
// 它要一个浏览器。CI 完全不用 node（前端产物进了版本控制，Go 直接嵌它），为了
// 这一层去装一个 chromium 换不到相应的价值。所以它和那条要真 token 的 e2e 一样：
// **开发者本地跑**，见 mock/ui_check.sh。
//
// 跑法由 ui_check.sh 张罗（起沙箱、起 daemon、调这个脚本）；这里认两个参数：
// 界面地址（带尾斜杠），以及沙箱的 state.json（冲突那一段要**从外面**改它，
// 扮演那个抢先改文件的命令行）。断言失败即非零退出，红的理由直接打在屏幕上。
//
// # 怎么找东西
//
// 一律按**机器标记**找，不按翻译过的文字：侧栏那一行有 `title="<source>"`、tab 有
// `title="<concept id>"`。按文字找的话，这个脚本会跟着语言跑（它跑在 zh-Hans 下），
// 而改一句译文就让检查红——那种红没有任何信息量。

import fs from "node:fs";

const url = process.argv[2];
const stateFile = process.argv[3];
if (!url || !stateFile) {
  console.error("用法: node ui_check.mjs <界面地址> <沙箱的 state.json 路径>");
  process.exit(2);
}
// 不带尾斜杠的那一份：它是**给人敲的**形式，也是 2026-09-20 那次真 bug 的现场
// （相对地址会拿它当基准去解析，于是 API 请求掉出 /ui 挂载点）。
const bare = url.replace(/\/+$/, "");

// 依赖解析：默认从 node_modules 找 playwright；这台机器上它可能装在别处，
// 那种情况用 PLAYWRIGHT_MODULE 指过去（与别处「机器相关的路径走环境变量」同一条）。
async function loadPlaywright() {
  const tries = [process.env.PLAYWRIGHT_MODULE, "playwright"].filter(Boolean);
  for (const spec of tries) {
    try {
      return await import(spec);
    } catch {
      /* 试下一个 */
    }
  }
  console.error(
    "找不到 playwright。装一个（它**不在**本仓库的依赖里，理由见文件头）：\n" +
      "  cd modules/web-dashboard/web && pnpm add -D playwright\n" +
      "已经装在别处的话，用环境变量指过去：PLAYWRIGHT_MODULE=/路径/playwright/index.mjs",
  );
  process.exit(2);
}

const { chromium } = await loadPlaywright();

let pass = 0;
let fail = 0;
const ok = (m) => { console.log(`  ✓ ${m}`); pass++; };
const bad = (m) => { console.log(`  ✗ ${m}`); fail++; };
const check = (m, cond, detail = "") => (cond ? ok(m) : bad(`${m}${detail ? " — " + detail : ""}`));
const skip = (m) => console.log(`  · ${m}`);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });

// 页面自己抛的错一个都不许有：白屏、组件崩了、effect 里炸了，全都先在这里露头。
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));
page.on("console", (m) => {
  if (m.type() !== "error") return;
  const text = m.text();
  // 409 是冲突那一段**预期**的结果：浏览器为任何非 2xx 的 fetch 都打一条 console
  // error，而「基线过期」正是那里要制造的局面。它不是页面抛的错——把它算进来的
  // 话，这条断言就变成了「不许测冲突」。排除得很窄，别的错照样算。
  if (text.includes("status of 409")) return;
  pageErrors.push("console: " + text);
});

// 有未保存的改动时，界面会挂一个 beforeunload 问询（见 App.svelte）。Playwright
// 默认**自动消掉**所有对话框，而消掉 beforeunload 的意思是「留在原地」——那样
// 下面那次 reload 会静静不发生，之后的断言全在测一个没刷新过的页面。所以这里
// 明确接受它。别的类型的对话框一个都不该出现（出现了就是 bug），记成页面错误。
page.on("dialog", (d) => {
  if (d.type() === "beforeunload") return void d.accept();
  pageErrors.push(`dialog(${d.type()}): ${d.message()}`);
  void d.dismiss();
});

// 后端那份快照：栏目名、概念 id 都从这儿取（**不猜翻译**）。
async function fetchSnapshot() {
  const r = await fetch(bare + "/api/snapshot");
  return r.json();
}

// —— 0. 敲 `/ui` 不带尾斜杠也要能用 ——
//
// 这一条是真 bug 的回归：porthub 认裸前缀，所以 `/ui` 进得来、页面画得出来（资源
// 地址是绝对的），但**运行期的 API 地址**是从文档路径解析的——从 `/ui` 出发，
// `api/snapshot` 成了 `/api/snapshot`，掉出挂载点、落进数据面，被当成一次形状不
// 认识的转发（400）。症状是「界面好好的、一操作就报错」，很难联想到 URL 上。
{
  const p = await browser.newPage({ viewport: { width: 1400, height: 900 } });
  const errs = [];
  p.on("pageerror", (e) => errs.push(String(e)));
  await p.goto(bare, { waitUntil: "networkidle" });
  await p.waitForTimeout(800);
  const n = await p.locator("section.card").count();
  check("`/ui`（不带尾斜杠）也能用", n > 0, `实际 ${n} 张卡；地址 ${p.url()}`);
  check("`/ui` 的请求落回了 /ui/", new URL(p.url()).pathname.startsWith("/ui/"), p.url());
  const banner = await p.locator(".banner").count();
  check("`/ui` 下没有报错横幅", banner === 0, await p.locator(".banner").first().innerText().catch(() => ""));
  check("`/ui` 下没有页面错误", errs.length === 0, errs.slice(0, 2).join(" / "));
  await p.close();
}

await page.goto(url, { waitUntil: "networkidle" });
await page.waitForTimeout(1200);

const snap = await fetchSnapshot();
const titleOf = (source) => (snap.sections.find((s) => s.source === source) || {}).title || "";

// —— 1. 渲染出来了 ——
const cards = await page.locator("section.card").count();
check("卡片渲染出来了", cards > 0, `实际 ${cards} 张`);
check("页面没有抛错", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

// —— 2. 不再是「一条瀑布」 ——
//
// 改版前：所有卡片按来源分组一路往下铺，实测 4303px ≈ 4.8 屏；「改一个开关」这件
// 四步能做完的事，第一步是滚三屏。判据两条：分组标题没了、整页高度在一屏上下
// （宽屏上内容多时留一点余量）。
{
  const headers = await page.locator("h2.srch").count();
  check("分组标题（一条瀑布的化石）没有了", headers === 0, `还有 ${headers} 个`);
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  console.log(`  · 整页高度 ${h}px（改版前 4303px ≈ 4.8 屏）`);
  check("整页高度回到一屏内", h < 900 * 1.5, `实际 ${h}px`);
  const side = await page.locator("nav.side button").count();
  check("侧栏列出了各节", side > 0, `实际 ${side} 行`);
}

// —— 3. 一屏只有一种语言 ——
//
// 这条是 2026-09-20 那个 bug 的棘轮：t() 读的语言住在模块级变量里、不是响应式
// 状态，于是没有响应式依赖的字符串停在**首次渲染**那一刻的语言上——后端给
// zh-Hans 时，同一行里「44 个概念」是中文而 "save" 是英文。
//
// 判据用**界面骨架上的几个词**（按钮、输入框提示）：它们是「没有响应式依赖」的
// 那一批，正是会冻住的那一批。语言不是英文时，它们就不该还是英文原文。
const lang = snap.lang;
const saveText = (await page.locator("header.top button.primary").innerText()).trim();
const placeholder = await page.locator("header.top input.filter").getAttribute("placeholder");
if (lang === "en" || !lang) {
  skip(`语言是 ${lang}，跳过「只有一种语言」那条`);
} else {
  // 键就是那句英文，所以「还是英文原文」= 没翻。
  check("按钮跟着界面语言走", saveText !== "save", `保存按钮显示 ${JSON.stringify(saveText)}`);
  check("输入框提示跟着界面语言走", !placeholder.startsWith("filter"), `实际 ${JSON.stringify(placeholder)}`);
}

// —— 4. 栏目名来自后端（模块自己报的），不是来源名 ——
//
// 掉掉它不会有任何东西报错：前端会回落到来源名，侧栏里出现 config / plugin-manager
// 这种机器标记，看着只像「还没翻」。所以在这一层按「和快照里的 title 一致、且不等于
// source」验一遍。
if (snap.sections.length) {
  const first = snap.sections[0];
  const shown = (await page.locator(`nav.side button[title="${first.source}"]`).first().innerText()).trim();
  check("侧栏用的是后端报的栏目名", shown.startsWith(first.title), `屏幕上是 ${JSON.stringify(shown)}，快照里是 ${JSON.stringify(first.title)}`);
  if (lang !== "en" && lang) {
    check("栏目名翻过了（不是来源名）", shown !== first.source, `屏幕上还是 ${JSON.stringify(shown)}`);
  }
}

// —— 5. 分栏：同一份文件的两半并排 ——
//
// 这是「改配置」最常见的动作（这个值在文件里长什么样），改版前要滚过去再滚回来。
{
  // 按**数据**找一对（与 nav.ts 的 fileOf 同一条约定：控件那半带 file、原文那半
  // 带 path，同一个相对路径就是同一份文件）。不写死 config —— 那是模块名，测里
  // 认了它，界面里那条「不认识模块」的规矩就只剩一半。
  const fileOf = (c) => c.data?.file ?? c.data?.path;
  const pair = snap.concepts.find(
    (c) =>
      c.kind !== "code" &&
      fileOf(c) &&
      snap.concepts.some((x) => x.kind === "code" && x.source === c.source && fileOf(x) === fileOf(c)),
  );
  if (!pair) {
    skip("这份装配里没有「同一份文件的两半」，跳过并排那条");
  } else {
    await page.locator(`nav.side button[title="${pair.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`button.tab[title="${pair.id}"]`).click().catch(() => {});
    await page.waitForTimeout(300);
    const split = await page.locator(".split.two").count();
    const panes = await page.locator(".pane-l, .pane-r").count();
    check("控件与原文并排了", split > 0 && panes === 2, `实际 ${panes} 栏`);
    const right = await page.locator(".pane-r .mono").first().innerText().catch(() => "");
    check("右栏是那份文件的原文", right.includes(fileOf(pair)), `右栏是 ${JSON.stringify(right)}`);
  }
}

// —— 6. 点一下真的落盘 ——
//
// 只测**可写的 toggles**（布尔开关）：它是「最少点击」那条设计里的一步操作，
// 也是用户在界面上改运行期行为的那条路。
//
// 改版后它在一节里、一张 tab 后面——所以先走过去。**这一步本身就是版面那场改版
// 的验收**：侧栏一次、tab 一次、开关一次、保存一次 = 四次操作（改版前是滚三屏）。
const box = page.locator('section.card input[type="checkbox"]').first();
async function openSwitches() {
  const src = snap.sections.find((s) => s.source === "plugin-manager");
  const sw = snap.concepts.find((c) => c.id === "plugin-manager.switches");
  if (!src || !sw) return false;
  await page.locator(`nav.side button[title="${src.source}"]`).click();
  await page.waitForTimeout(200);
  await page.locator(`button.tab[title="${sw.id}"]`).click();
  await page.waitForTimeout(250);
  return true;
}
if (!(await openSwitches())) {
  skip("这份装配里没有 plugin-manager 的开关卡，跳过保存与冲突那几段");
} else {
  await box.click();
  await page.waitForTimeout(200);
  const before = (await page.locator("header.top button.primary").innerText()).trim();
  check("改一下就出现「未保存」", /\d/.test(before), `保存按钮显示 ${JSON.stringify(before)}`);

  // 换到别节再回来：草稿**不许丢**（版面改了，这条语义没改）。
  const conf = snap.sections.find((s) => s.source !== "plugin-manager");
  if (conf) {
    await page.locator(`nav.side button[title="${conf.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`nav.side button[title="plugin-manager"]`).click();
    await page.waitForTimeout(200);
    const still = (await page.locator("header.top button.primary").innerText()).trim();
    check("换节再回来，草稿还在", /\d/.test(still), `保存按钮显示 ${JSON.stringify(still)}`);

    // 顺带：选中的那一节进了 URL，刷新之后还在（「少操作」的一部分）。
    //
    // 注意草稿**不过**刷新：它住在页面内存里，而刷新就是重来（App 里那条
    // beforeunload 会在真刷新之前拦一下，见那里的注释）。所以这里刷完要重新点
    // 一下开关，再造一个草稿出来——这不是将就，是这条语义本来的样子。
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForTimeout(900);
    const active = await page.locator("nav.side button.on").getAttribute("title");
    check("刷新之后还在原来那一节", active === "plugin-manager", `实际 ${active}`);
    await openSwitches();
    await box.click();
    await page.waitForTimeout(200);
  }

  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(900);
  const after = (await page.locator("header.top button.primary").innerText()).trim();
  check("保存之后回到「没有未保存的东西」", !/\d/.test(after), `保存按钮显示 ${JSON.stringify(after)}`);

  // —— 7. 别人抢先改过 → 弹冲突，而且**不许覆盖** ——
  //
  // 这是这个产品里唯一一处两个写者改同一份文件的地方（命令行与浏览器），也是当初
  // 点名要的语义：「commit 的时候会检测是否冲突，如果有，提示用户」。
  //
  // 浏览器这一层独有的一条：**对话框真的出现在用户面前了**。Go 那条 e2e 验的是
  // 409 与那几个字段，看不见屏幕上是弹了一张带两边原文的卡片，还是一片空白。
  const raw = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  raw.written_by_someone_else = true;
  fs.writeFileSync(stateFile, JSON.stringify(raw, null, 2));

  await box.click();
  await page.waitForTimeout(150);
  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(900);

  const dialog = page.locator(".banner.conflict");
  const shown = await dialog.count();
  check("别人抢先改过之后弹出了冲突", shown > 0, "屏幕上什么都没弹，用户的改动就这么没了");
  if (shown) {
    // 两边原文都要摆出来，否则用户没法判断该留谁的。
    const text = await dialog.first().innerText();
    check("冲突里点名了文件", text.includes(stateFile), text.slice(0, 80));
    check("冲突给了两个选择", /keep mine/i.test(text) || text.includes("保留"), text.slice(0, 80));
  }
  const after2 = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  check("被拒的那次写没有覆盖磁盘", after2.written_by_someone_else === true);
}

// —— 8. 键盘：`/` 找东西、Esc 退出 ——
//
// 这条是「4-5 次操作」那条线的下限保障：鼠标走完侧栏 → tab → 控件 → 保存是四次，
// 没有余量；`/` 与 Alt+数字 把「回到一个已知位置」压成一次按键。
{
  await page.keyboard.press("/");
  await page.waitForTimeout(150);
  const focused = await page.evaluate(() => document.activeElement?.className || "");
  check("`/` 把光标送进过滤框", focused.includes("filter"), `焦点在 ${JSON.stringify(focused)}`);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);
  const after = await page.evaluate(() => document.activeElement?.tagName || "");
  check("Esc 退出输入", after !== "INPUT", `焦点还在 ${after}`);
}

check("整场没有页面错误", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
await browser.close();
process.exit(fail === 0 ? 0 : 1);
