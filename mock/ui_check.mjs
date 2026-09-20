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
// 界面地址，以及沙箱的 state.json（冲突那一段要**从外面**改它，扮演那个抢先改
// 文件的命令行）。断言失败即非零退出，红的理由直接打在屏幕上。

import fs from "node:fs";

const url = process.argv[2];
const stateFile = process.argv[3];
if (!url || !stateFile) {
  console.error("用法: node ui_check.mjs <界面地址> <沙箱的 state.json 路径>");
  process.exit(2);
}

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

await page.goto(url, { waitUntil: "networkidle" });
await page.waitForTimeout(1200);

// —— 1. 渲染出来了 ——
const cards = await page.locator("section.card").count();
check("卡片渲染出来了", cards > 0, `实际 ${cards} 张`);
check("页面没有抛错", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

// —— 2. 一屏只有一种语言 ——
//
// 这条是 2026-09-20 那个 bug 的棘轮：t() 读的语言住在模块级变量里、不是响应式
// 状态，于是没有响应式依赖的字符串停在**首次渲染**那一刻的语言上——后端给
// zh-Hans 时，同一行里「44 个概念」是中文而 "save" 是英文。
//
// 判据用**界面骨架上的几个词**（按钮、输入框提示）：它们是「没有响应式依赖」的
// 那一类，正是会冻住的那一批。语言不是英文时，它们就不该还是英文原文。
const lang = await page.evaluate(async (u) => {
  const r = await fetch(u.replace(/\/$/, "") + "/api/snapshot");
  return (await r.json()).lang;
}, url);
const saveText = (await page.locator("header.top button.primary").innerText()).trim();
const placeholder = await page.locator("header.top input.filter").getAttribute("placeholder");
if (lang === "en" || !lang) {
  console.log(`  · 语言是 ${lang}，跳过「只有一种语言」那条`);
} else {
  // 键就是那句英文，所以「还是英文原文」= 没翻。
  check("按钮跟着界面语言走", saveText !== "save", `保存按钮显示 ${JSON.stringify(saveText)}`);
  check("输入框提示跟着界面语言走", !placeholder.startsWith("filter"), `实际 ${JSON.stringify(placeholder)}`);
}

// —— 3. 点一下真的落盘 ——
//
// 只测**可写的 toggles**（布尔开关）：它是「最少点击」那条设计里的一步操作，
// 也是用户在界面上改运行期行为的那条路。
const box = page.locator('section.card:has-text("plugin-manager.switches") input[type="checkbox"]').first();
if ((await box.count()) === 0) {
  console.log("  · 这份装配里没有布尔开关，跳过保存那两条");
} else {
  await box.click();
  await page.waitForTimeout(200);
  const before = (await page.locator("header.top button.primary").innerText()).trim();
  check("改一下就出现「未保存」", /\d/.test(before), `保存按钮显示 ${JSON.stringify(before)}`);
  await page.locator("header.top button.primary").click();
  await page.waitForTimeout(900);
  const after = (await page.locator("header.top button.primary").innerText()).trim();
  check("保存之后回到「没有未保存的东西」", !/\d/.test(after), `保存按钮显示 ${JSON.stringify(after)}`);
}

// —— 4. 别人抢先改过 → 弹冲突，而且**不许覆盖** ——
//
// 这是这个产品里唯一一处两个写者改同一份文件的地方（命令行与浏览器），也是当初
// 点名要的语义：「commit 的时候会检测是否冲突，如果有，提示用户」。
//
// 浏览器这一层独有的一条：**对话框真的出现在用户面前了**。Go 那条 e2e 验的是
// 409 与那几个字段，看不见屏幕上是弹了一张带两边原文的卡片，还是一片空白。
if ((await box.count()) === 0) {
  console.log("  · 这份装配里没有布尔开关，跳过冲突那两条");
} else {
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForTimeout(600);
  // 扮演那个「抢先改文件的命令行」：往 state.json 里塞一个键。塞一个**别人写的、
  // 浏览器不知道**的键，这样「有没有被覆盖」是可以判定的。
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
  const after = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  check("被拒的那次写没有覆盖磁盘", after.written_by_someone_else === true);
}

check("整场没有页面错误", pageErrors.length === 0, pageErrors.slice(0, 2).join(" / "));

console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
await browser.close();
process.exit(fail === 0 ? 0 : 1);
