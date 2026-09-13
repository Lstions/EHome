import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:390,height:844}, locale:'zh-CN', hasTouch:true, colorScheme:'light' });
const page = await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
await page.goto(BASE+'/edge-device',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
await page.locator('button[aria-label="表格视图"]').first().click().catch(()=>{}); await page.waitForTimeout(1500);
const out = await page.evaluate(()=>{
  const qa=s=>[...document.querySelectorAll(s)];
  const R=e=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),y:Math.round(r.y),w:Math.round(r.width),h:Math.round(r.height),right:Math.round(r.right),bottom:Math.round(r.bottom)};};
  const tr=qa('.el-table__row')[0];
  const cell=tr.querySelector('td.el-table-fixed-column--right');
  const cellBox=R(cell);
  const btns=[...cell.querySelectorAll('button')].map(b=>({cls:b.className.slice(0,50), icon:b.querySelector('svg')?'svg':'none', ...R(b)}));
  const inner=cell.querySelector('.cell');
  // EL 的 cell 内容是否溢出
  return { rowBox:R(tr), cellBox, cellInnerW:inner?Math.round(inner.getBoundingClientRect().width):null,
    cellContentW: inner?inner.scrollWidth:null, cellContentH: inner?inner.scrollHeight:null,
    btns, btnBottomMax: Math.max(...btns.map(b=>b.bottom)), rowBottom: Math.round(tr.getBoundingClientRect().bottom),
    overflowBeyondCell: btns.some(b=>b.bottom>cellBox.bottom+1) };
});
console.log(JSON.stringify(out,null,1));
await browser.close();
