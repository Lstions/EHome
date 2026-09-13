/**
 * 定向复跑 15（键盘可达性 + 对比度）：
 *  A. 本域各页 cursor:pointer 元素中 tabIndex<0 的比例 + 具体元素与尺寸（规范 §3.1.3 MUST）。
 *  B. 亮/暗两主题下浅底状态徽标与次级文字的实际对比度（WCAG AA 正文 4.5:1、大字 3:1）。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = { results: [] };
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });

const LUM = [
  '(function(){',
  '  function p(c){c=c/255;return c<=0.03928?c/12.92:Math.pow((c+0.055)/1.055,2.4)}',
  '  function L(r,g,b){return 0.2126*p(r)+0.7152*p(g)+0.0722*p(b)}',
  '  function parse(s){',
  '    var i=String(s).indexOf("("); if(i<0) return null;',
  '    var inner=String(s).slice(i+1, String(s).indexOf(")"));',
  '    var a=inner.split(",").map(function(x){return parseFloat(x)});',
  '    if(a.length<3||isNaN(a[0])) return null;',
  '    return {r:a[0],g:a[1],b:a[2],a:a.length>3?a[3]:1};',
  '  }',
  '  function ratio(f,b){var F=parse(f),B=parse(b); if(!F||!B) return null;',
  '    var l1=L(F.r,F.g,F.b), l2=L(B.r,B.g,B.b); var hi=Math.max(l1,l2), lo=Math.min(l1,l2);',
  '    return Math.round(((hi+0.05)/(lo+0.05))*100)/100; }',
  '  window.__ratio=ratio; window.__parse=parse;',
  '})();'
].join('\n');

for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: theme === 'dark' ? 'dark' : 'light' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(t => { localStorage.setItem('theme', t); }, theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', 'admin');
  await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await sleep(1500);

  for (const [nm, route] of [['automation', '/automation'], ['alerts', '/alerts'], ['device-configs', '/device-configs'], ['data-sources', '/data-sources'], ['firmware', '/firmware']]) {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(2000);
    await page.addScriptTag({ content: LUM });
    const kbd = await page.evaluate(() => {
      const all = [...document.querySelectorAll('*')].filter(el => getComputedStyle(el).cursor === 'pointer' && el.offsetParent !== null);
      const bad = all.filter(el => el.tabIndex < 0);
      const detail = bad.slice(0, 14).map(el => {
        const r = el.getBoundingClientRect();
        return { tag: el.tagName.toLowerCase(), cls: String(el.className || '').slice(0, 44), text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 18),
          w: Math.round(r.width), h: Math.round(r.height), role: el.getAttribute('role'), tabindex: el.getAttribute('tabindex'), ariaLabel: el.getAttribute('aria-label') };
      });
      const byClass = {};
      for (const el of bad) { const k = el.tagName.toLowerCase() + '.' + String(el.className || '').trim().split(/\s+/).slice(0, 2).join('.'); byClass[k] = (byClass[k] || 0) + 1; }
      return { pointerTotal: all.length, notFocusable: bad.length, byClass, detail,
        nonNativeClickable: all.filter(el => !['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName)).length };
    });
    const contrast = await page.evaluate(() => {
      const sel = ['.el-tag', '.page-header h2', '.page-header-subtitle', '.stat-label', '.stat-value', '.card-title', '.el-table th', '.el-table td', '.el-button--primary', '.hint', '.el-descriptions__label', '.el-table__empty-text'];
      const res = [];
      for (const s of sel) {
        const el = document.querySelector(s); if (!el) continue;
        let bgEl = el, bg = 'rgba(0, 0, 0, 0)';
        while (bgEl && (bg === 'rgba(0, 0, 0, 0)' || bg === 'transparent')) { bg = getComputedStyle(bgEl).backgroundColor; bgEl = bgEl.parentElement; }
        const cs = getComputedStyle(el);
        res.push({ sel: s, text: (el.textContent || '').trim().slice(0, 16), color: cs.color, bg, fontSize: cs.fontSize, fontWeight: cs.fontWeight, ratio: window.__ratio(cs.color, bg) });
      }
      const tagRes = [];
      for (const t of ['', 'success', 'warning', 'danger', 'info', 'primary']) {
        const el = document.querySelector('.el-tag' + (t ? '.el-tag--' + t : ':not([class*="el-tag--"])'));
        if (!el) continue;
        const cs = getComputedStyle(el);
        tagRes.push({ type: t || 'default', color: cs.color, bg: cs.backgroundColor, fontSize: cs.fontSize, ratio: window.__ratio(cs.color, cs.backgroundColor), text: (el.textContent || '').trim().slice(0, 12) });
      }
      return { samples: res, tags: tagRes };
    });
    out.results.push({ theme, page: nm, route, kbd, contrast });
  }
  await ctx.close();
}
await browser.close();
fs.writeFileSync('/tmp/uiux-d3-kbd/kbd-contrast.json', JSON.stringify(out, null, 2));
for (const r of out.results) {
  console.log('===== ' + r.theme + ' | ' + r.page + ' ===== pointer=' + r.kbd.pointerTotal + ' notFocusable=' + r.kbd.notFocusable + ' nonNative=' + r.kbd.nonNativeClickable);
  console.log('  byClass: ' + JSON.stringify(r.kbd.byClass));
  console.log('  contrast: ' + r.contrast.samples.map(s => s.sel + '=' + s.ratio + '(' + s.fontSize + ')').join(' | '));
  console.log('  tags: ' + r.contrast.tags.map(t => t.type + '=' + t.ratio).join(' | '));
}