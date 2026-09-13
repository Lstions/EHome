import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};
for (const [vp,W,H] of [['desktop-1440',1440,900],['mobile-390',390,844],['mobile-360',360,800],['tablet-768',768,1024]]) {
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
  const rec={};
  // KPI 栅格
  await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
  rec.kpi = await page.evaluate(()=>{
    const g=document.querySelector('.stats-row'); const cs=getComputedStyle(g);
    const cards=[...g.children].map(c=>{const r=c.getBoundingClientRect(); return {w:Math.round(r.width),h:Math.round(r.height)};});
    const icon=document.querySelector('.stat-icon'); const val=document.querySelector('.stat-value'); const lab=document.querySelector('.stat-label');
    const gapY = cards.length>1 ? Math.round(g.children[1].getBoundingClientRect().y - g.children[0].getBoundingClientRect().y) : null;
    return { cols: cs.gridTemplateColumns, gap: cs.gap, cardCount: cards.length, cards,
      iconW: icon?Math.round(icon.getBoundingClientRect().width):null,
      valFs: val?getComputedStyle(val).fontSize:null, labFs: lab?getComputedStyle(lab).fontSize:null,
      totalW: Math.round(g.getBoundingClientRect().width), twoRows: gapY!==null && gapY>0.5 && gapY < cards[0].h+40 };
  });
  // node-list 刷新 loading 状态（CDP 网络节流确保 loading 可观测）
  const cdp = await ctx.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 2000, downloadThroughput: 20000, uploadThroughput: 20000 });
  await page.locator('button:has-text("刷新")').first().click().catch(()=>{});
  await page.waitForTimeout(700);
  rec.refreshLoading = await page.evaluate(()=>{
    const qa=s=>[...document.querySelectorAll(s)];
    const btn=qa('button').find(b=>(b.textContent||'').trim()==='刷新');
    return { btnLoadingClass: btn?btn.className:null, btnDisabled: btn?btn.disabled:null,
      ariaBusy: qa('[aria-busy]').map(e=>e.tagName.toLowerCase()+'.'+(e.className||'').toString().slice(0,30)+'='+e.getAttribute('aria-busy')),
      cardBtnsDisabled: qa('.collector-card button:disabled').length, cardBtns: qa('.collector-card button').length,
      spinner: qa('.is-loading').length };
  });
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await page.waitForTimeout(3200);
  // node detail tabs
  await page.goto(BASE+'/node/1',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
  rec.tabs = await page.evaluate(()=>[...document.querySelectorAll('.tab-item')].map(t=>({t:t.textContent.trim(),role:t.getAttribute('role'),tabindex:t.getAttribute('tabindex'),ariaSelected:t.getAttribute('aria-selected'),cursor:getComputedStyle(t).cursor})));
  // device-configs 编辑弹窗字段
  await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
  rec.cfgEmpty = await page.evaluate(()=>({rows:document.querySelectorAll('.el-table__row').length, cards:document.querySelectorAll('.config-card').length, empty:document.querySelectorAll('.empty-state, .el-empty').length, text:(document.body.innerText||'').replace(/\s+/g,' ').slice(0,220)}));
  out[vp]=rec;
  await ctx.close();
}
await browser.close();
console.log(JSON.stringify(out,null,1));
