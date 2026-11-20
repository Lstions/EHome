import { chromium } from "/mnt/storage/WorkSpace/EHome/frontend-shared/node_modules/.pnpm/playwright-core@1.62.0/node_modules/playwright-core/index.mjs";
import path from "node:path";
const ROOT = "/mnt/storage/WorkSpace/EHome/designs/node-page-redesign-2026-11-19";
const b = await chromium.launch({ executablePath: process.env.HOME + "/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome", args: ["--no-sandbox"] });
const p = await b.newPage({ viewport: { width: 1440, height: 1000 } });
const errors = [];
p.on("pageerror", e => errors.push(String(e)));
p.on("console", m => { if (m.type() === "error") errors.push(m.text()); });
await p.goto("file://" + path.join(ROOT, "index.html"));
await p.waitForTimeout(300);
const base = await p.evaluate(() => Array.from(document.querySelectorAll(".tab")).map(t => ({ label: t.textContent.trim(), w: Math.round(t.getBoundingClientRect().width * 100) / 100, fw: getComputedStyle(t).fontWeight })));
const after = {};
for (const t of ["periph", "dma", "devices", "ota", "logs", "terminal", "basic", "bus"]) {
  await p.click("[data-tab=\"" + t + "\"]");
  await p.waitForTimeout(120);
  after[t] = await p.evaluate(() => Array.from(document.querySelectorAll(".tab")).map(t => ({ label: t.textContent.trim(), w: Math.round(t.getBoundingClientRect().width * 100) / 100, fw: getComputedStyle(t).fontWeight })));
}
const drift = [];
for (const [state, rows] of Object.entries(after)) {
  rows.forEach((r, i) => { if (r.w !== base[i].w) drift.push(state + ":" + r.label + " " + base[i].w + "->" + r.w); });
}
const weights = Array.from(new Set(base.map(x => x.fw)));
const typo = await p.evaluate(() => {
  const sels = ["body", ".node-title", ".tab", ".btn", ".stat b", ".tbl", ".tbl th", ".res-name", ".detail-title", ".pill", ".hint", ".detail-note"];
  const out = {};
  sels.forEach(s => { const el = document.querySelector(s); if (el) { const cs = getComputedStyle(el); out[s] = cs.fontSize + "/" + cs.fontWeight + "/" + cs.fontFamily.split(",")[0]; } });
  return out;
});
const sizes = await p.evaluate(() => Array.from(new Set(Array.from(document.querySelectorAll("*")).map(el => getComputedStyle(el).fontSize))).sort((a,b)=>parseFloat(a)-parseFloat(b)));
console.log(JSON.stringify({ tabBase: base, drift, weights, typo, sizes, errors }, null, 2));
await b.close();
if (drift.length || errors.length) process.exitCode = 1;