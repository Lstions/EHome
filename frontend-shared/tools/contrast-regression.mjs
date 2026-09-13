/**
 * WCAG 对比度回归检查（主控新增，2026-09-13）。
 *
 * 动机：对比度修复任务改善了亮色（EP 默认调色板白字 2.78 → 5.98），但**漏了暗色**：
 * 暗色块沿用 EP 默认 --color-primary(#409eff)，主按钮白字仍只有 2.78。
 * 且修暗色基色后又引出 el-tag primary 浅底徽标 2.7（同一个变量承担
 * 「实心填充色」与「浅底文字色」两个相反角色）。两次都是**主控独立复验**发现的 ——
 * 子代理自报的"已完成"没有覆盖这两处。
 *
 * 因此把复验固化为工具：任何人改主题 token 后都应跑一次。
 *
 * 用法（先 pnpm build 并确保后端在 8082 提供 dist + 真实数据）：
 *   cd frontend-shared && node tools/contrast-regression.mjs
 * 退出码非 0 表示有元素不达标。
 */
import { chromium } from '@playwright/test';

const BASE = process.env.CONTRAST_BASE || 'http://127.0.0.1:8082';
const USER = process.env.CONTRAST_USER || 'admin';
const PASS = process.env.CONTRAST_PASS || 'UiuxAudit2026!';
// 审计报告点名的失败元素 + 修复后应保持达标的元素。
const SELECTORS = ['.el-tag--success', '.el-tag--warning', '.el-tag', '.el-button--primary', '.el-table th .cell', '.ws-status'];
// 正文阈值 4.5；表头等「大字/次要」允许 3.0（WCAG AA 大字）。
const THRESHOLD = { '.el-table th .cell': 3.0 };
const DEFAULT_THRESHOLD = 4.5;

const lum = ([r, g, b]) => {
  const f = c => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
};
const ratio = (a, c) => { const l1 = lum(a), l2 = lum(c); return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05); };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox'] });
const rows = [];
for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(t => localStorage.setItem('theme', t), theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await page.waitForTimeout(2000);
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  const items = await page.evaluate(sels => {
    const parse = s => { const m = s.match(/rgba?\(([^)]+)\)/); if (!m) return null; return m[1].split(',').map(x => parseFloat(x)).slice(0, 3); };
    // 有效背景：元素自身背景透明时向上找第一个非透明祖先（审计踩过的坑）。
    const effBg = el => { let n = el.parentElement; while (n && n !== document.documentElement) { const bg = parse(getComputedStyle(n).backgroundColor); if (bg) return bg; n = n.parentElement; } return [255, 255, 255]; };
    const out = [];
    for (const sel of sels) {
      const el = document.querySelector(sel);
      if (!el) continue;
      const cs = getComputedStyle(el);
      const fg = parse(cs.color);
      const bg = parse(cs.backgroundColor) || effBg(el);
      if (fg && bg) out.push({ sel, fg, bg, text: (el.textContent || '').trim().slice(0, 16) });
    }
    return out;
  }, SELECTORS);
  for (const it of items) rows.push({ theme, ...it, ratio: ratio(it.fg, it.bg) });
  await ctx.close();
}
await browser.close();

console.log('元素'.padEnd(26) + '主题'.padEnd(7) + '对比度'.padEnd(9) + '阈值'.padEnd(7) + '判定  文本');
let bad = 0;
for (const r of rows) {
  const need = THRESHOLD[r.sel] ?? DEFAULT_THRESHOLD;
  const ok = r.ratio >= need;
  if (!ok) bad++;
  console.log(String(r.sel).padEnd(26) + r.theme.padEnd(7) + String(Math.round(r.ratio * 100) / 100).padEnd(9) + String(need).padEnd(7) + (ok ? 'PASS' : 'FAIL') + '  ' + r.text);
}
if (bad > 0) { console.error('\n有 ' + bad + ' 个元素不达标'); process.exit(1); }
console.log('\n全部达标。');
