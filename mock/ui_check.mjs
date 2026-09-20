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
// 明确接受它。
//
// confirm 也接受：删掉一份档位文件这类不可撤销的动作**该**先问一句（见
// MappingEditor 的删除按钮），问了并把问题记下来，正好让下面能断言「它问过」。
// 别的一律算页面错误——一个没人预期的模态框就是 bug。
const confirms = [];
page.on("dialog", (d) => {
  if (d.type() === "beforeunload") return void d.accept();
  if (d.type() === "confirm") {
    confirms.push(d.message());
    return void d.accept();
  }
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
    await page.locator(`button.tab[title="${pair.id}"], nav.v button[title="${pair.id}"]`).first().click().catch(() => {});
    await page.waitForTimeout(300);
    const split = await page.locator(".split.two").count();
    const panes = await page.locator(".pane-l, .pane-r").count();
    check("控件与原文并排了", split > 0 && panes === 2, `实际 ${panes} 栏`);
    const right = await page.locator(".pane-r .mono").first().innerText().catch(() => "");
    check("右栏是那份文件的原文", right.includes(fileOf(pair)), `右栏是 ${JSON.stringify(right)}`);
  }
}

// —— 6. 卡一多，横 tab 让位成左侧竖栏 ——
//
// 这是「config 38 张横 tab 滚不动」那次的回归：超过阈值（App 的 TAB_OVERFLOW=8）
// 的节，tab 条改成一个竖向滚动列表放在内容左侧。阈值是内容决定的（不是宽度），
// 所以这里按「config 的卡数」判断走哪条断言——沙箱注入了 9 个填充 profile，正常
// 会走到竖栏那半。
{
  // 导航的单位是**文件**，不是卡片：同一份文件有「控件 + 原文」两半时只列控件那
  // 半，原文只是并排的右栏（见 App.svelte 的 navUnits）。所以这里的期望值不能直接
  // 数概念，要按同一条规则先折一遍——否则这条断言会把「原文不再占一格」这个**有意
  // 的改动**报成失败。
  const fileOf = (c) => c.data?.file ?? c.data?.path;
  const navUnits = (list) => {
    const byFile = new Map();
    for (const c of list) {
      const f = fileOf(c);
      if (!f) continue;
      const k = c.source + "|" + f;
      byFile.set(k, [...(byFile.get(k) ?? []), c]);
    }
    return list.filter((c) => {
      const f = fileOf(c);
      if (!f) return true;
      const control = byFile.get(c.source + "|" + f).find((g) => g.kind !== "code");
      return !(control && c.kind === "code");
    });
  };
  const cfgCards = navUnits(snap.concepts.filter((c) => c.source === "config"));
  const n = cfgCards.length;
  await page.locator(`nav.side button[title="config"]`).click();
  await page.waitForTimeout(400);
  const rows = await page.locator(".content nav.v").count();
  const horiz = await page.locator(".content .tabs").count();
  if (n > 8) {
    check("卡一多就转成左侧竖栏", rows === 1, `nav.v 出现 ${rows} 次`);
    check("竖栏出现时没有横 tab 条", horiz === 0, `还剩 ${horiz} 条横的`);
    const items = await page.locator(".content nav.v button").count();
    check("竖栏列出这节全部卡", items === n, `${items} vs ${n}`);
    // 竖栏必须是滚动容器。回归点不在「这次真的溢出了没」（沙箱的卡数够不够挤满
    // 一屏是内容的事），而在「容器是不是设成了可以滚」——用户那次 bug 是 38 行
    // 摆出来却永远不会滚，因为里面那层高度跟着内容走。机制在做，溢出才生效。
    const slotCss = await page.evaluate(() => {
      const slot = document.querySelector(".content.subcol > .nav-slot");
      return slot ? getComputedStyle(slot).overflowY : "";
    });
    check("竖栏是滚动容器（滚轮能生效）", slotCss === "auto", `overflow-y=${slotCss}`);
    // 点竖栏里一张卡要真的切过去（从**会出现在竖栏里**的那批里挑——原文半不在里面）
    const target = cfgCards[1];
    await page.locator(`.content nav.v button[title="${target.id}"]`).click();
    await page.waitForTimeout(300);
    const on = await page.locator(".content nav.v button.on").getAttribute("title");
    check("点竖栏里一张卡能切过去", on === target.id, `实际选中 ${on}`);

    // —— 分组：大档（`档位`）与族（`claude`）——
    //
    // 用户要的是「装完就要配的两项」与「一天天加出来的一堆」在版面上分开（原话：
    // 「profile 这里就可以做一个分栏了啊」）。标题由后端的两级 group 给
    // （`档位/claude`），画不画由这里按「这一段底下有没有 ≥2 张卡」定。
    //
    // 判据不能只是「有标题」：一行小字下头卡片不缩进的话，那只是一句注释，读的人
    // 还是分不出那两张属于它。所以这里量**缩进**——标题之后的每一张卡都必须比标题
    // 本身更靠右。
    const groups = await page.locator(".content nav.v .group").count();
    check("竖栏里有分组标题（大档 / 一族档位）", groups > 0, `实际 ${groups} 个`);
    const nesting = await page.evaluate(() => {
      const nav = document.querySelector(".content nav.v");
      const kids = [...nav.children];
      const at = kids.findIndex((e) => e.classList.contains("group"));
      if (at < 0) return null;
      const head = parseFloat(getComputedStyle(kids[at]).paddingLeft);
      const after = kids.slice(at + 1).filter((e) => e.classList.contains("row"));
      return {
        head,
        n: after.length,
        flat: after
          .filter((e) => parseFloat(getComputedStyle(e).paddingLeft) <= head)
          .map((e) => e.textContent.trim().slice(0, 24)),
      };
    });
    check(
      "标题底下的卡真的缩进了（不是只加了一行小字）",
      !!nesting && nesting.n > 0 && nesting.flat.length === 0,
      JSON.stringify(nesting),
    );
  } else {
    skip(`这份装配里 config 卡没超阈值（${n} ≤ 8），竖栏那几条不适用`);
  }
}

// —— 7. 点一下真的落盘 ——
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
  await page.locator(`button.tab[title="${sw.id}"], nav.v button[title="${sw.id}"]`).first().click();
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

  // —— 8. 别人抢先改过 → 弹冲突，而且**不许覆盖** ——
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

// —— 9. provider 表：凭据不出门，但能加能改 ——
//
// 这一段是「用户要在网页上配 provider」那条要求的验收。它验的是一件看起来很矛盾
// 的事：**浏览器拿不到那个 key，却仍然能改 provider**。做法是值根本不进快照
// （FieldSecret），界面回传空串 = 别动它。所以这里两条都要验：
//
//   - 那一格来时是空的、类型是 password（凭据不出门）；
//   - 改完别处保存之后，磁盘上的 key **还在**（空串没有被当成「删掉」）。
//
// 第二条错了的话，症状是「改一次 base_url，下一次请求 401」，而且保存是成功的。
{
  const providersFile = stateFile.replace(/state\.json$/, "providers.json");
  const before = fs.existsSync(providersFile) ? fs.readFileSync(providersFile, "utf8") : "";
  const keyBefore = (JSON.parse(before || "{}").providers?.demo?.api_key) ?? "";

  const card = snap.concepts.find((c) => c.kind === "records" && c.data?.file === "providers.json");
  if (!card) {
    skip("这份装配里没有 provider 表那张卡，跳过 provider 那几条");
  } else {
    await page.locator(`nav.side button[title="${card.source}"]`).click();
    await page.waitForTimeout(200);
    await page.locator(`button.tab[title="${card.id}"], nav.v button[title="${card.id}"]`).first().click();
    await page.waitForTimeout(300);

    const keyBox = page.locator('section.card input[type="password"]').first();
    check("凭据那一格是遮蔽的", (await keyBox.count()) > 0);
    check("凭据的值没有随快照过来", (await keyBox.inputValue()) === "", "浏览器手里不该有这个值");

    // 改一个**不是凭据**的字段，保存，然后看磁盘。
    //
    // 上一段故意制造的那个冲突还挂在屏幕上（那是它要验的东西），所以「没有报错」
    // 的判据是**横幅没有变多**，不是「屏幕上一个横幅都没有」。
    const bannerBefore = await page.locator(".banner").first().innerText().catch(() => "");
    // 字段 id 是 `f-<这条记录的 id>-<字段名>`（记录 id 现在是 provider 名，不再是
    // 数组下标——见 kinds/Records.svelte 里 key/id 分开的那段注释）。所以按**后缀**
    // 找，别把 id 写死。
    const baseURL = page.locator(`input[id$="-base_url"]`).first();
    await baseURL.fill("http://127.0.0.1:9/changed");
    await page.waitForTimeout(150);
    await page.locator("header.top button.primary").click();
    await page.waitForTimeout(900);
    // 保存报错要说出来。少了这一条，「没写下去」与「写了但我读错了」在屏幕上长得
    // 一模一样——文件没变的真实原因会被猜成十几种。
    const banner = await page.locator(".banner").first().innerText().catch(() => "");
    check("保存没有多出新的报错", banner === bannerBefore, `${banner.slice(0, 120)}`.trim());
    const after = JSON.parse(fs.readFileSync(providersFile, "utf8")).providers?.demo ?? {};
    check("改得动 provider 的字段", after.base_url === "http://127.0.0.1:9/changed", JSON.stringify(after));
    check("回传空的凭据格没有把 key 弄丢", after.api_key === keyBefore && keyBefore !== "", `现在 ${JSON.stringify(after.api_key)}`);

    // 眼睛：点了之后输入框变明文。值是我们自己刚敲的，所以这条验的是「那个按钮真的
    // 接到了这一格」——粘一串 key 之后核对一眼，是最常见的动作。
    //
    // 按 **id** 找那一格（后缀匹配，理由同上），不按 `input[type=password]`：
    // 点亮之后类型就变了，用类型当选择器会变成「元素不存在」而超时——那看起来像
    // 界面坏了，其实是在测一个已经不存在的东西。
    const keyField = page.locator(`input[id$="-api_key"]`).first();
    await keyField.fill("sk-typed-in-the-browser");
    await page.waitForTimeout(150);
    await page.locator(".rec button[title]").first().click();
    await page.waitForTimeout(150);
    check("眼睛把那格变成明文", (await keyField.getAttribute("type")) === "text");
  }
}

// —— 10. 一份档位文件的全部动作都在它自己那张 kv 卡上 ——
//
// 2026-09-20 之前：新建/改名/删除在一张「档位文件」目录卡上，档位在每张 profile 卡
// 上——同一份文件被两张卡说着，而那张目录卡列的文件与这些卡一一对应（用户的原话：
// 「这个多余的啊……要求彻底删除」）。现在动作都收在卡自己身上：
// `+ profile` 建新的一份、名字那一格改名、继承那一格选父档位、`delete this file`
// 删掉整份文件（删之前问一句）。
{
  const home = stateFile.slice(0, stateFile.lastIndexOf("/"));
  const mappingsDir = home + "/mappings";
  const has = (n) => fs.existsSync(`${mappingsDir}/${n}.kv`);

  // 那张多余的目录卡必须**真的没了**（它是被点名要求删掉的东西，回来了要有人喊）。
  check(
    "「档位文件」那张多余的目录卡不在了",
    !snap.concepts.some((c) => c.id === "config.profiles"),
    snap.concepts.filter((c) => c.id.startsWith("config.profile")).map((c) => c.id).join(" "),
  );

  // 从一张白纸开始（前面几节故意制造过冲突，草稿会干扰这一节）。
  await page.goto(url, { waitUntil: "networkidle" });
  await page.waitForTimeout(700);

  const anyProfile = snap.concepts.find((c) => c.kind === "mapping-editor");
  await page.locator(`nav.side button[title="${anyProfile.source}"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="${anyProfile.id}"], nav.v button[title="${anyProfile.id}"]`)
    .first()
    .click();
  await page.waitForTimeout(400);

  // —— 新建：**这一节的工具条**上一个按钮 ——
  //
  // 它不在任何一张卡上（2026-09-21 从卡片头搬过去的）：新建出来的那一份此刻还没有
  // 概念，没有哪张卡能挂它。挂在栏目上它还永远够得着，不管你正看着哪一张卡。
  await page.locator(".secbar button").first().click();
  // 两件事都要等，而且是**先落盘、再切卡**（切卡发生在 apply 之后的 load 里）。
  // 只等文件的话会在导航之前就断言 URL，那红的是测试的时序不是产品。
  for (let i = 0; i < 40 && !has("new-profile"); i++) await page.waitForTimeout(100);
  check("`+ profile` 建出了一份新档位文件", has("new-profile"), `mappings 里有 ${fs.readdirSync(mappingsDir).join(" ")}`);
  for (let i = 0; i < 40 && !page.url().includes("config.profile.new-profile"); i++) {
    await page.waitForTimeout(100);
  }
  check("新建之后直接切到那一张卡", page.url().includes("config.profile.new-profile"), page.url());

  // —— 改名 + 继承：两格都在卡片头上，改完点保存一起落盘 ——
  await page.locator(".card .head input.name").fill("uicheck-renamed");
  await page.locator(".card .head select").selectOption("demo");
  await page.waitForTimeout(200);
  await page.locator("header.top button.primary").click();
  for (let i = 0; i < 40 && !has("uicheck-renamed"); i++) await page.waitForTimeout(100);
  check("改名把文件改到了新名字下", has("uicheck-renamed") && !has("new-profile"),
    `new-profile=${has("new-profile")} uicheck-renamed=${has("uicheck-renamed")}`);
  const renamed = has("uicheck-renamed") ? fs.readFileSync(`${mappingsDir}/uicheck-renamed.kv`, "utf8") : "";
  check("继承写进了文件", renamed.includes("extends=demo"), JSON.stringify(renamed.slice(0, 80)));

  // —— 删除：问一句，然后真的删盘 ——
  const before = confirms.length;
  await page
    .locator(`button.tab[title="config.profile.uicheck-renamed"], nav.v button[title="config.profile.uicheck-renamed"]`)
    .first()
    .click();
  await page.waitForTimeout(350);
  await page.locator(".card .head button.danger").click();
  for (let i = 0; i < 30 && has("uicheck-renamed"); i++) await page.waitForTimeout(100);
  check("删之前问了一句", confirms.length > before, `confirms=${JSON.stringify(confirms.slice(before))}`);
  check("删除真的删掉了文件", !has("uicheck-renamed"));
}


// —— 11.5. 一份文件只有一个可写的面 ——
//
// 这一条锁的是**整块复杂度的来源被拆掉了**。之前：同一份文件的两个半边都能改，
// 于是要有「谁后改的说了算」、要挤出输的那一半的草稿、还要让它跟着显示赢的那一半
// 的内容——为此长出了内核的 Preview 契约、BFF 的 /api/preview、前端的防抖与预览
// 表。而它们没有一个与「配置」有关。
//
// 规则改成：**有结构化控件的那份文件，它原文那一半只读**（没有结构化控件的，
// 原文才是写入口）。判据落在「那一栏上有没有只读标记」与「它到底能不能改」。
{
  const pair = snap.concepts.find(
    (c) => c.kind === "mapping-editor" && snap.concepts.some((x) => x.kind === "code" && (x.data.file ?? x.data.path) === c.data.file),
  );
  if (!pair) {
    skip("这份装配里没有「控件 + 原文」的一对，跳过 11.5");
  } else {
    const rawID = "config.file." + pair.data.file;
    const raw = snap.concepts.find((c) => c.id === rawID);
    check("同一份文件的原文半边是**只读**的（写入口只有控件那一个）", raw && !raw.writable,
      `原文半边 writable=${raw && raw.writable}`);

    // 界面上也要真的这么显示：那一栏上有只读标记，而且编辑器不接受输入。
    await page.locator(`nav.side button[title="${pair.source}"]`).click();
    await page.waitForTimeout(250);
    await page.locator(`button.tab[title="${pair.id}"], nav.v button[title="${pair.id}"]`).first().click();
    await page.waitForTimeout(400);
    const pill = await page.locator(".pane-r .pill", { hasText: /只读|read-only/ }).count();
    check("原文那一栏上写着「只读」", pill > 0, "屏幕上没有任何只读标记，用户会以为它能改");
    const before = await page.locator(".pane-r .cm-content").first().innerText();
    await page.locator(".pane-r .cm-content").first().click();
    await page.keyboard.press("Control+End");
    await page.keyboard.type("XYZ");
    await page.waitForTimeout(300);
    const after = await page.locator(".pane-r .cm-content").first().innerText();
    check("在只读的原文里打字打不进去", after === before, `多出了 ${JSON.stringify(after.slice(-12))}`);
    check("打不进去也就不会有草稿", (await page.locator("header.top button.primary").innerText()).trim() === "保存",
      "保存按钮上有数字，说明只读那一栏冒出了一份草稿");
  }
}

// —— 12. 运行期开关画成「开关」，不是复选框 ——
//
// 用户的原话是「别搞那个傻逼钩啊，用一个可以 toggle 的开关」。判据落在
// `role=switch` 上：读屏器与键盘靠它，而一个纯 CSS 的假开关不会有。
{
  await page.locator(`nav.side button[title="plugin-manager"]`).click();
  await page.waitForTimeout(250);
  await page
    .locator(`button.tab[title="plugin-manager.switches"], nav.v button[title="plugin-manager.switches"]`)
    .first()
    .click();
  await page.waitForTimeout(300);
  const switches = await page.locator(`.card .body input[role="switch"]`).count();
  const plain = await page.locator(`.card .body input[type="checkbox"]:not([role="switch"])`).count();
  check("开关是开关（role=switch）", switches > 0, `实际 ${switches} 个`);
  check("没有一个是裸复选框", plain === 0, `还有 ${plain} 个裸复选框`);
}

// —— 13. 键盘：`/` 找东西、Esc 退出 ——
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

// —— 14. 侧栏的分组标题底下，真的全是那一类 ——
//
// 用户的原话：「大的目录是不是也要分类下啊」。分组由各模块**声明**
// （view.Section.Group），侧栏按它聚拢。
//
// 判据不是「画出了标题」而是**成员真的聚在一起**：第一版是「边走边插标题」，而成
// 员按来源序来，于是「数据面」标题底下只有「熔断」、「网关」跑到了下一个标题底下
// （实测）。标题说的是「下面这几栏是一类」，底下就必须真的是那一类——一行小字
// 加错了地方，读的人反而更晕。
//
// 不归组的那几栏排在最前面（今天只有「配置」）：它们不属于任何标题，所以游标从
// 空串起步，正好也对得上。
{
  if (!snap.sections.some((s) => s.group)) {
    skip("这份装配里没有模块声明分组，跳过侧栏分组那条");
  } else {
    const layout = await page.evaluate(() =>
      [...document.querySelector("nav.side").children]
        .filter((e) => e.classList.contains("group") || e.classList.contains("row"))
        .map((e) => ({
          head: e.classList.contains("group"),
          // 标题读文字、栏读 title（= source，机器标记，不跟着语言跑）。
          name: e.classList.contains("group") ? e.textContent.trim() : e.getAttribute("title"),
        })),
    );
    const groupOf = new Map(snap.sections.map((s) => [s.source, s.group ?? ""]));
    let cur = "";
    let heads = 0;
    const stray = [];
    for (const r of layout) {
      if (r.head) {
        heads++;
        cur = r.name;
        continue;
      }
      if ((groupOf.get(r.name) ?? "") !== cur) stray.push(r.name);
    }
    check("侧栏画出了分组标题", heads > 0, `实际画了 ${heads} 个`);
    check(
      "每个标题底下只有那一类的栏（没被别的组打断）",
      stray.length === 0,
      `跑错组的: ${stray.join(", ")}`,
    );
  }
}

// —— 15. 静默刷新之后，卡片的顺序不许变 ——
//
// 用户的原话：「上游、全局设置经常会自己跑到下面」。真相不是它们「自己跑」，而是
// **界面在局部刷新时重排了一遍**：3 秒一次的静默刷新只问 Live 的那几个源，拿回来
// 的几张卡要并回手里那份，拼接这一步自己排了一次——而它当时只按 `(source, id)`
// 排。于是 `Order` 为 0/1 的「全局设置」「上游」掉到十几张档位卡（Order 10）底下。
//
// 症状之所以是「经常」而不是「总是」：整份加载（刷新页面）走的是另一条路，那条
// 不排、照抄后端顺序，所以是对的。只有静默刷新之后错——而它 3 秒就来一次。
//
// 所以这条必须**等一次真的静默刷新**再比：@3 秒是 App 里的间隔常量，这里按它等。
{
  await page.locator(`nav.side button[title="config"]`).click();
  await page.waitForTimeout(300);
  const navOrder = () =>
    page.evaluate(() =>
      [...document.querySelectorAll("nav.v button.row")].map((b) => b.getAttribute("title")),
    );
  const before = await navOrder();
  check("config 那节的竖栏画出来了", before.length > 2, `实际 ${before.length} 张`);
  // 后端声明的顺序：全局设置(0) / 上游(1) 在档位卡(10)前面。
  check(
    "装完就要配的那两张排在最前",
    before.findIndex((id) => id === "config.providers") <
      before.findIndex((id) => id?.startsWith("config.profile.")),
    before.slice(0, 4).join(" "),
  );
  // **必须真的开一次自动刷新**：轮询默认是关的（`auto = false`），不打开的话这条
  // 检查是空转的——它会「通过」，而它要防的那个 bug 一次都没被触发过。第一版就是
  // 这样：把排序改回旧写法，它照样全绿。
  await page.locator('header.top input[type="checkbox"]').check();
  // 轮询 3 秒一次（App 里的间隔），等一次落地再多给一点。
  await page.waitForTimeout(4200);
  const after = await navOrder();
  check(
    "静默刷新之后顺序没变",
    JSON.stringify(after) === JSON.stringify(before),
    `刷新前 ${before.slice(0, 4).join(" ")} / 刷新后 ${after.slice(0, 4).join(" ")}`,
  );
  check(
    "静默刷新之后那两张还在最前",
    after.findIndex((id) => id === "config.providers") <
      after.findIndex((id) => id?.startsWith("config.profile.")),
    after.slice(0, 4).join(" "),
  );
  await page.locator('header.top input[type="checkbox"]').uncheck();
}

check("整场没有页面错误", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
await browser.close();
process.exit(fail === 0 ? 0 : 1);
