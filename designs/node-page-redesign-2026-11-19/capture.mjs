import { chromium } from "/mnt/storage/WorkSpace/EHome/frontend-shared/node_modules/.pnpm/playwright-core@1.62.0/node_modules/playwright-core/index.mjs";
import path from "node:path";
const ROOT = "/mnt/storage/WorkSpace/EHome/designs/node-page-redesign-2026-11-19";
const OUT = path.join(ROOT, "shots");
const URL = "file://" + path.join(ROOT, "index.html");
const browser = await chromium.launch({ executablePath: process.env.HOME + "/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome", args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"] });
const errors = [];
const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2 });
const page = await ctx.newPage();
page.on("console", m => { if (m.type() === "error") errors.push(m.text()); });
page.on("pageerror", e => errors.push(String(e)));
await page.goto(URL);
await page.waitForTimeout(400);
const tabs = ["bus", "periph", "dma", "devices", "ota", "logs", "terminal"];
for (const t of tabs) {
  await page.click("[data-tab=\"" + t + "\"]");
  await page.waitForTimeout(180);
  await page.screenshot({ path: path.join(OUT, "desktop-" + t + ".png") });
}
await page.click("[data-tab=\"bus\"]");
await page.waitForTimeout(150);
await page.screenshot({ path: path.join(OUT, "desktop-bus-full.png"), fullPage: true });
// I2C 空态
await page.click("[data-bus=\"i2c\"]");
await page.waitForTimeout(180);
await page.screenshot({ path: path.join(OUT, "desktop-bus-i2c.png") });
await page.click("[data-action=\"scan\"]");
await page.waitForTimeout(200);
await page.screenshot({ path: path.join(OUT, "desktop-bus-scan.png") });
// UART 抽屉
await page.click("[data-bus=\"uart\"]");
await page.waitForTimeout(150);
await page.click("[data-action=\"new-channel\"]");
await page.waitForTimeout(250);
await page.screenshot({ path: path.join(OUT, "desktop-bus-drawer.png") });
await page.click("[data-action=\"close-drawer\"]");
await page.waitForTimeout(200);
// 移动端
const mctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
const m = await mctx.newPage();
m.on("pageerror", e => errors.push("mobile:" + String(e)));
await m.goto(URL);
await m.waitForTimeout(400);
await m.screenshot({ path: path.join(OUT, "mobile-bus.png") });
await m.click("[data-tab=\"periph\"]");
await m.waitForTimeout(200);
await m.screenshot({ path: path.join(OUT, "mobile-periph.png") });
await m.click("[data-tab=\"dma\"]");
await m.waitForTimeout(200);
await m.screenshot({ path: path.join(OUT, "mobile-dma.png") });
await m.click("[data-tab=\"terminal\"]");
await m.waitForTimeout(200);
await m.screenshot({ path: path.join(OUT, "mobile-terminal.png") });
const counts = await page.evaluate(() => ({
  tabs: document.querySelectorAll(".tab").length,
  resources: document.querySelectorAll("[data-res]").length,
  channels: document.querySelectorAll("#resDetail tbody tr").length,
  termChannels: document.querySelectorAll(".chan-item").length,
  termLines: document.querySelectorAll("#termLog .txrx").length,
  logLines: document.querySelectorAll("#logTerm .ln").length,
  dmaRows: document.querySelectorAll("#dmaTable tbody tr").length
}));
console.log(JSON.stringify({ counts, errors }, null, 2));
await browser.close();
if (errors.length) process.exitCode = 1;