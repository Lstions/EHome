import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};
for (const [vp,W,H] of [['desktop-1440',1440,900],['mobile-390',390,844],['mobile-360',360,800]]) {
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
  const t0=Date.now();
  await page.goto(BASE+'/logical-device',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(6000);
  const ms=Date.now()-t0;
  out[vp] = await page.evaluate(()=>{
    const qa=s=>[...document.querySelectorAll(s)], q=s=>document.querySelector(s);
    const de=document.documentElement;
    const tbl=q('.el-table');
    const firstRow=qa('.el-table__row')[0];
    const fixedRow=firstRow?firstRow.querySelector('.el-table-fixed-column--right'):null;
    const R=e=>{if(!e)return null;const r=e.getBoundingClientRect();return{x:Math.round(r.x),w:Math.round(r.width),right:Math.round(r.right),y:Math.round(r.y)};};
    return { rows: qa('.el-table__row').length, bodyEls: qa('body *').length,
      pagination: qa('.el-pagination').length, docScrollH: de.scrollHeight, bodyScrollH: document.body.scrollHeight,
      loadMore: qa('.el-pagination, .infinite-scroll, [class*="load-more"]').length,
      tableR:R(tbl), fixedR:R(fixedRow),
      firstRowActionBtn:(()=>{const b=firstRow&&firstRow.querySelector('td:last-child button, .el-table-fixed-column--right button'); if(!b) return null; const r=b.getBoundingClientRect(); return {t:b.textContent.trim(), x:Math.round(r.x), right:Math.round(r.right), w:Math.round(r.width), h:Math.round(r.height)};})(),
      editBtnCount: qa('.el-table__row .el-button').length,
      vw: innerWidth };
  });
  out[vp].renderWaitMs = ms;
  await ctx.close();
}
await browser.close();
console.log(JSON.stringify(out,null,1));
