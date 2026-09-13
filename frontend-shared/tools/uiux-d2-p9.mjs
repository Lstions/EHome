import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'light' });
const page = await ctx.newPage();
const reqs=[], resps=[];
page.on('request', r => { if (r.url().includes('/api/v1/logical-devices')) reqs.push({m:r.method(),u:r.url(),auth: !!r.headers()['authorization']}); });
page.on('response', async r => { if (r.url().includes('/api/v1/logical-devices')) { let body=null; try{ body=(await r.text()).slice(0,300);}catch{} resps.push({status:r.status(),u:r.url(),body}); } });
page.on('pageerror', e=>console.log('PAGEERR', String(e).slice(0,200)));
page.on('console', m=>{ if(m.type()==='error') console.log('CONSOLE-ERR', m.text().slice(0,220)); });
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2200);
await page.goto(BASE+'/logical-device',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(4000);
const facts = await page.evaluate(()=>{
  const q=s=>document.querySelector(s), qa=s=>[...document.querySelectorAll(s)];
  return { rows: qa('.el-table__row').length, emptyTitle:q('.empty-title')?.textContent?.trim(),
    emptyDesc:q('.el-empty__description')?.textContent?.trim()?.slice(0,90),
    tableText:(q('.el-table')?.textContent||'').replace(/\s+/g,' ').trim().slice(0,150),
    mergeBtn:(q('.filter-right button')||{}).textContent?.trim(),
    pagination: qa('.el-pagination').map(p=>(p.textContent||'').replace(/\s+/g,' ').trim().slice(0,60)),
    hint: q('.mobile-table-hint')?.textContent?.trim() };
});
console.log('REQUESTS', JSON.stringify(reqs,null,1));
console.log('RESPONSES', JSON.stringify(resps,null,1));
console.log('FACTS', JSON.stringify(facts,null,1));
await browser.close();
