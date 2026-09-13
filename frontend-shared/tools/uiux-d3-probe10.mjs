/**
 * 定向复跑 10：
 *  A. data-sources 在 360/390/768 的固定操作列与其余列的命中测试（elementFromPoint），
 *     证明「名称/详情入口被固定列遮挡」而不是「只是视觉重叠」。
 *  B. el-drawer 在窄屏的宽度（theme.css 只兜底 .el-dialog，未兜底 .el-drawer）。
 *  C. 清理 audit_probe_* 临时数据。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 360, height: 800 }, locale: 'zh-CN' });
const page = await ctx.newPage();
const out = [];
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);
const tok = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token'));

// 清理历史临时数据
const pre = await page.evaluate(async (t) => {
  let n = 0;
  for (const cat of ['audit_probe_tmp', 'audit_probe_drawer']) {
    const j = await (await fetch('/api/v1/data-sources?category=' + cat, { headers: { Authorization: 'Bearer ' + t } })).json();
    for (const it of (j?.data?.items || [])) { await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); n++; }
  }
  return n;
}, tok);
out.push({ step: 'pre-clean', removed: pre });
const created = await page.evaluate(async (t) => (await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_drawer', edge_device_id: 7053, name: '抽屉探测来源', max_fail_count: 3 }) })).status, tok);
out.push({ step: 'create', status: created });

for (const w of [360, 390, 768, 1024]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  const hit = await page.evaluate(() => {
    const t = document.querySelector('.el-table');
    const wrap = t ? t.querySelector('.el-scrollbar__wrap') : null;
    const fixedTd = document.querySelector('td.el-table-fixed-column--right');
    const nameBtn = document.querySelector('[data-test="ds-detail"]');
    const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), top: Math.round(x.top), bottom: Math.round(x.bottom), w: Math.round(x.width), h: Math.round(x.height) }; })() : null;
    let probe = null;
    if (nameBtn) {
      const b = nameBtn.getBoundingClientRect();
      const cx = b.left + b.width / 2, cy = b.top + b.height / 2;
      const inViewport = cx >= 0 && cx <= window.innerWidth && cy >= 0 && cy <= window.innerHeight;
      const el = inViewport ? document.elementFromPoint(cx, cy) : null;
      probe = { centerX: Math.round(cx), centerY: Math.round(cy), inViewport, hitTag: el ? el.tagName.toLowerCase() : null,
        hitCls: el ? String(el.className || '').slice(0, 60) : null, hitIsNameBtn: el ? (el === nameBtn || nameBtn.contains(el)) : null,
        hitIsFixedCol: el ? !!el.closest('td.el-table-fixed-column--right') : null };
    }
    const tw = t ? t.getBoundingClientRect() : null;
    const fw = fixedTd ? fixedTd.getBoundingClientRect() : null;
    return {
      viewportW: window.innerWidth, viewportH: window.innerHeight,
      tableBox: rr(t), tableCW: t ? t.clientWidth : null,
      wrapCW: wrap ? wrap.clientWidth : null, wrapSW: wrap ? wrap.scrollWidth : null, wrapOX: wrap ? getComputedStyle(wrap).overflowX : null,
      fixedTd: rr(fixedTd),
      fixedCoversViewportPct: (tw && fw) ? Math.round(Math.min(fw.width, tw.width) / tw.width * 100) : null,
      nameBtn: rr(nameBtn), nameProbe: probe,
      mobileHint: !!document.querySelector('.mobile-table-hint'), mobileWrapper: !!document.querySelector('.mobile-table-wrapper'),
      rowCount: document.querySelectorAll('.el-table__row').length,
    };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/hit-' + w + '.png' });
  // 用 force 打开抽屉，量 el-drawer
  let drawer = null;
  try {
    await page.click('[data-test="ds-detail"]', { force: true, timeout: 4000 });
    await sleep(1500);
    drawer = await page.evaluate(() => {
      const d = document.querySelector('.el-drawer');
      const b = document.querySelector('.el-drawer__body');
      const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width), top: Math.round(x.top), bottom: Math.round(x.bottom) }; })() : null;
      const descs = document.querySelector('.el-drawer .el-descriptions');
      return { found: !!d, box: rr(d), cssWidth: d ? getComputedStyle(d).width : null, maxWidth: d ? getComputedStyle(d).maxWidth : null,
        sizeAttr: d ? d.getAttribute('size') : null, overflows: d ? (d.getBoundingClientRect().left < -1) : null,
        bodyCW: b ? b.clientWidth : null, bodySW: b ? b.scrollWidth : null, descsBox: rr(descs), descsSW: descs ? descs.scrollWidth : null, descsCW: descs ? descs.clientWidth : null,
        docSW: document.documentElement.scrollWidth, docCW: document.documentElement.clientWidth, viewportW: window.innerWidth };
    });
    await page.screenshot({ path: '/tmp/uiux-d3f/drawer-' + w + '.png' });
    await page.keyboard.press('Escape'); await sleep(600);
  } catch (e) { drawer = { error: String(e).slice(0, 120) }; }
  out.push({ step: 'measure', w, hit, drawer, screenshots: ['/tmp/uiux-d3f/hit-' + w + '.png', '/tmp/uiux-d3f/drawer-' + w + '.png'] });
}
const cleanup = await page.evaluate(async (t) => {
  const j = await (await fetch('/api/v1/data-sources?category=audit_probe_drawer', { headers: { Authorization: 'Bearer ' + t } })).json();
  const items = j?.data?.items || []; const st = [];
  for (const it of items) { st.push((await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } })).status); }
  return { found: items.map(i => i.id), statuses: st };
}, tok);
out.push({ step: 'cleanup', cleanup });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3j-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log('===== ' + r.step + ' ' + (r.w || '') + ' ====='); console.log(JSON.stringify(r).slice(0, 1500)); }
