/**
 * D2 可点击非原生容器取证（精确版）。
 * cursor 会继承，直接扫 body * 会把所有后代都算进来 → 这里只取 "pointer 根"
 * （自身 cursor:pointer 且父元素 cursor 不是 pointer），即真正承载点击的元素。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d2-probe';
const ROUTES=['/node','/channel','/edge-device','/logical-device','/node/1','/edge-device/7053','/device-configs','/monitor'];

const PROBE = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const vis = e => { const r=e.getBoundingClientRect(); return r.width>0&&r.height>0&&getComputedStyle(e).visibility!=='hidden'; };
  const desc = e => e.tagName.toLowerCase() + (typeof e.className==='string'&&e.className ? '.'+e.className.trim().split(/\s+/).slice(0,3).join('.') : '');
  const isPtr = e => e && getComputedStyle(e).cursor === 'pointer';
  const all = qa('body *').filter(vis);
  const ptr = all.filter(isPtr);
  // pointer 根：自身可点，父元素不可点（父元素可能是 body/html）
  const roots = ptr.filter(e => !isPtr(e.parentElement));
  const NATIVE = /^(a|button|input|select|textarea|summary|area|option|label)$/;
  const EP_BTN = e => e.classList.contains('el-button') || e.closest('.el-button') === e || e.getAttribute('role')==='button';
  const rows = roots.map(e => {
    const r = e.getBoundingClientRect();
    const native = NATIVE.test(e.tagName.toLowerCase()) || EP_BTN(e);
    const hasRole = !!e.getAttribute('role');
    const hasTab = e.tabIndex >= 0;
    return { sel: desc(e), text:(e.textContent||'').trim().replace(/\s+/g,' ').slice(0,28),
      w:Math.round(r.width), h:Math.round(r.height), native,
      role:e.getAttribute('role'), tabindex:e.getAttribute('tabindex'), ariaLabel:e.getAttribute('aria-label'),
      focusable: hasTab, compliant: native || (hasRole && hasTab) };
  });
  const nonNative = rows.filter(r => !r.native);
  return {
    pointerRootTotal: roots.length,
    nonNativeTotal: nonNative.length,
    nonNativeNonCompliant: nonNative.filter(r=>!r.compliant).length,
    nonNativeRows: nonNative,
  };
};

const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out = {};
for (const vp of [['desktop-1440',1440,900],['mobile-390',390,844]]) {
  const [name,W,H] = vp;
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  out[name]={};
  for (const rt of ROUTES) {
    await page.goto(BASE+rt,{waitUntil:'domcontentloaded'}); await page.waitForTimeout(rt.startsWith('/edge-device/')?4800:2300);
    out[name][rt] = await page.evaluate(PROBE);
  }
  await ctx.close();
}
await browser.close();
fs.writeFileSync(OUT+'/p16-a11y-roots.json', JSON.stringify(out,null,2));
console.log('done');
