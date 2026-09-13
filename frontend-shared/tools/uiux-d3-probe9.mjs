/**
 * 定向复跑 9：data-sources「数据源详情」抽屉在 360px 的宽度
 * （theme.css 只给 .el-dialog 做了 92vw 兜底，el-drawer 未覆盖；§4.4.1 要求 360px 可完成核心操作）。
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
const created = await page.evaluate(async (t) => {
  const r = await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_drawer', edge_device_id: 7053, name: '抽屉探测来源', max_fail_count: 3 }) });
  return r.status;
}, tok);
out.push({ step: 'create', status: created });

for (const w of [360, 390, 768]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  // 点击名称进入详情抽屉
  await page.click('[data-test="ds-detail"]');
  await sleep(1600);
  const geo = await page.evaluate(() => {
    const d = document.querySelector('.el-drawer');
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width), top: Math.round(x.top), bottom: Math.round(x.bottom) }; })() : null;
    const body = document.querySelector('.el-drawer__body');
    const descs = document.querySelector('.el-drawer .el-descriptions');
    return {
      viewportW: window.innerWidth,
      drawerFound: !!d, drawer: r(d),
      drawerCssWidth: d ? getComputedStyle(d).width : null, drawerMaxW: d ? getComputedStyle(d).maxWidth : null, drawerSize: d ? d.getAttribute('size') : null,
      bodyBox: r(body), bodyCW: body ? body.clientWidth : null, bodySW: body ? body.scrollWidth : null, bodyOX: body ? getComputedStyle(body).overflowX : null,
      descsBox: r(descs), descsCW: descs ? descs.clientWidth : null, descsSW: descs ? descs.scrollWidth : null,
      docScrollW: document.documentElement.scrollWidth, docClientW: document.documentElement.clientWidth,
      overflowsViewport: d ? d.getBoundingClientRect().right > window.innerWidth + 1 || d.getBoundingClientRect().left < -1 : null,
      timelineCount: document.querySelectorAll('.el-timeline').length,
    };
  });
  const shot = '/tmp/uiux-d3f/drawer-' + w + '.png';
  await page.screenshot({ path: shot });
  out.push({ step: 'drawer-geo', w, screenshot: shot, ...geo });
  await page.keyboard.press('Escape'); await sleep(700);
}
const cleanup = await page.evaluate(async (t) => {
  const r = await fetch('/api/v1/data-sources?category=audit_probe_drawer', { headers: { Authorization: 'Bearer ' + t } });
  const j = await r.json(); const items = (j && j.data && j.data.items) || []; const st = [];
  for (const it of items) { const d = await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); st.push(d.status); }
  return { found: items.map(i => i.id), statuses: st };
}, tok);
out.push({ step: 'cleanup', cleanup });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3i-facts.json', JSON.stringify(out, null, 2));
for (const r of out) console.log(JSON.stringify(r).slice(0, 900));
