/**
 * 定向复跑 16：把「键盘不可达」的假阳性剥离干净。
 *  cursor:pointer 的**后代**（按钮内的 span/i/svg/path）不是独立控件，
 *  必须排除「其自身或祖先已是可聚焦原生控件(button/a/input/select/textarea)」的元素，
 *  否则会把 260 个 <span> 误报成 260 个缺陷（契约 §2.2 的镜像错误）。
 *  同时做真实 Tab 序列实测：本域主要操作能否被键盘到达。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = { pages: [], tab: [] };
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

const SCAN = () => {
  const NATIVE = ['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA'];
  const isNativeOrInside = el => { let n = el; while (n && n !== document.body) { if (NATIVE.includes(n.tagName)) return true; n = n.parentElement; } return false; };
  const main = document.querySelector('.main-content') || document.body;
  const all = [...main.querySelectorAll('*')].filter(el => getComputedStyle(el).cursor === 'pointer' && el.offsetParent !== null);
  const nonNative = all.filter(el => !NATIVE.includes(el.tagName));
  const real = nonNative.filter(el => !isNativeOrInside(el));
  const det = real.slice(0, 10).map(el => { const r = el.getBoundingClientRect(); return { tag: el.tagName.toLowerCase(), cls: String(el.className || '').slice(0, 40), text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 20), w: Math.round(r.width), h: Math.round(r.height), role: el.getAttribute('role'), tabindex: el.getAttribute('tabindex'), ariaLabel: el.getAttribute('aria-label') }; });
  const shellAll = [...document.querySelectorAll('*')].filter(el => getComputedStyle(el).cursor === 'pointer' && el.offsetParent !== null && !main.contains(el));
  return { mainPointer: all.length, mainNonNative: nonNative.length, realDefects: real.length, detail: det,
    realByClass: real.reduce((a, el) => { const k = el.tagName.toLowerCase() + '.' + String(el.className || '').trim().split(/\s+/).slice(0, 2).join('.'); a[k] = (a[k] || 0) + 1; return a; }, {}),
    shellPointer: shellAll.length,
    // 交互控件本身是否可聚焦（真正的判据）
    controls: [...main.querySelectorAll('button, [role="button"], .el-switch, input, select, textarea, a[href]')].slice(0, 400).map(c => ({ tag: c.tagName.toLowerCase(), cls: String(c.className || '').slice(0, 34), text: (c.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 12), tabIndex: c.tabIndex, ariaLabel: c.getAttribute('aria-label'), disabled: c.disabled === true || c.classList.contains('is-disabled') })).filter(c => c.tabIndex < 0) };
};

for (const [nm, route] of [['automation', '/automation'], ['alerts', '/alerts'], ['device-configs', '/device-configs'], ['data-sources', '/data-sources'], ['firmware', '/firmware']]) {
  await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  out.pages.push({ page: nm, ...(await page.evaluate(SCAN)) });
}

// 真实 Tab 序列：从 automation 页首开始按 40 次 Tab，记录落点
await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
await sleep(2200);
await page.evaluate(() => document.body.focus());
const seq = [];
for (let i = 0; i < 40; i++) {
  await page.keyboard.press('Tab');
  const cur = await page.evaluate(() => { const a = document.activeElement; if (!a) return null; return { tag: a.tagName.toLowerCase(), cls: String(a.className || '').slice(0, 36), text: (a.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 16), aria: a.getAttribute('aria-label'), inMain: !!a.closest('.main-content'), inSidebar: !!a.closest('aside, .sidebar, .el-menu') }; });
  seq.push(cur);
}
out.tab = seq;
await browser.close();
fs.writeFileSync('/tmp/uiux-d3-kbd/kbd-precise.json', JSON.stringify(out, null, 2));
for (const p of out.pages) { console.log('===== ' + p.page + ' ====='); console.log('  mainPointer=' + p.mainPointer + ' mainNonNative=' + p.mainNonNative + ' realDefects=' + p.realDefects + ' shellPointer=' + p.shellPointer);
  console.log('  realByClass: ' + JSON.stringify(p.realByClass));
  for (const d of p.detail.slice(0, 5)) console.log('    ' + JSON.stringify(d));
  console.log('  unfocusableControls(tabIndex<0, first 6): ' + JSON.stringify(p.controls.slice(0, 6))); }
console.log('=== TAB SEQUENCE (automation, 40 presses) ===');
for (let i = 0; i < out.tab.length; i++) { const t = out.tab[i]; console.log(String(i + 1).padStart(3) + ' ' + (t ? (t.tag + ' | ' + (t.aria || t.text || t.cls) + ' | main=' + t.inMain + ' side=' + t.inSidebar) : 'null')); }