// 临时探针：把「新增一条 → 保存 → 删掉它 → 保存」两次 /ui/api/apply 的**请求体**
// 打出来。用来判定 removed[] 到底有没有进 payload（applyProfiles 只按显式 removed
// 删文件——只少一条记录是不会删文件的）。
import fs from "node:fs";

const url = process.argv[2];
const stateFile = process.argv[3];
const home = stateFile.slice(0, stateFile.lastIndexOf("/"));
const mappingsDir = home + "/mappings";

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright");
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });

page.on("request", (r) => {
  if (r.url().includes("/ui/api/apply")) {
    console.log("APPLY body:", r.postData());
  }
});

await page.goto(url, { waitUntil: "networkidle" });
await page.waitForTimeout(800);
await page.locator(`nav.side button[title="config"]`).click();
await page.waitForTimeout(300);
await page
  .locator(`button.tab[title="config.profiles"], nav.v button[title="config.profiles"]`)
  .first()
  .click();
await page.waitForTimeout(400);

const rows = () => page.locator(".card .body .rec").count();
const before = await rows();
await page.locator(".card .body > button").last().click();
await page.waitForTimeout(300);
console.log("rows", before, "->", await rows());
await page.locator(".card .body .rec").last().locator("input").first().fill("probe-x");
await page.waitForTimeout(250);
console.log("--- save #1 (create) ---");
await page.locator("header.top button.primary").click();
await page.waitForTimeout(1500);
console.log("file exists:", fs.existsSync(`${mappingsDir}/probe-x.kv`));
console.log("banner:", await page.locator(".banner").first().innerText().catch(() => "-"));

const row = page.locator(`.card .body .rec:has(b:text-is("probe-x"))`).first();
console.log("row count matching probe-x:", await page.locator(`.card .body .rec:has(b:text-is("probe-x"))`).count());
console.log("buttons inside that row:", await row.locator("button").count());
for (let i = 0; i < (await row.locator("button").count()); i++) {
  console.log(`  button[${i}] text=`, JSON.stringify((await row.locator("button").nth(i).innerText()).trim()),
    "title=", JSON.stringify(await row.locator("button").nth(i).getAttribute("title")));
}
console.log("--- click remove ---");
await row.locator("button").first().click();
await page.waitForTimeout(300);
console.log("rows after remove click:", await rows());
console.log("--- save #2 (delete) ---");
await page.locator("header.top button.primary").click();
await page.waitForTimeout(1500);
console.log("file still exists:", fs.existsSync(`${mappingsDir}/probe-x.kv`));
console.log("banner:", await page.locator(".banner").first().innerText().catch(() => "-"));
console.log("rows matching probe-x after save:", await page.locator(`.card .body .rec:has(b:text-is("probe-x"))`).count());

await browser.close();
