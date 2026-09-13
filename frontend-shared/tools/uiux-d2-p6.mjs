import { chromium } from '@playwright/test';
import fs from 'node:fs'; import path from 'node:path';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};
async function run(theme,W,H,VP){
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:theme==='dark'?'dark':'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2200);
  const rec={};
  // A) /node/1 面包屑（局部 + 全局）
  await page.goto(BASE+'/node/1',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
  rec.breadcrumb = await page.evaluate(()=>{
    const q=s=>document.querySelector(s); const R=e=>{if(!e)return null;const r=e.getBoundingClientRect();return{w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y)};};
    const local=q('.no-breadcrumb'), glob=q('.breadcrumb');
    return { local:R(local), localText:(local?.textContent||'').replace(/\s+/g,' ').trim(), localDisplay: local?getComputedStyle(local).display:null,
             global:R(glob), globalText:(glob?.textContent||'').replace(/\s+/g,' ').trim(), globalDisplay: glob?getComputedStyle(glob).display:null };
  });
  // B) 禁用按钮 tooltip 是否给出原因（hover）
  const btn = page.locator('.ph-actions .btn').first();
  const box = await btn.boundingBox();
  if (box) { await page.mouse.move(box.x+box.width/2, box.y+box.height/2); await page.waitForTimeout(1200); }
  rec.disabledTooltip = await page.evaluate(()=>{
    const t=[...document.querySelectorAll('.el-popper, .el-tooltip__popper')].filter(p=>getComputedStyle(p).display!=='none' && p.getBoundingClientRect().width>0);
    return t.map(p=>(p.textContent||'').trim().slice(0,60));
  });
  const f1=path.join('/tmp/uiux-d2-probe',VP+'-'+theme+'-p6-node1-breadcrumb.png'); await page.screenshot({path:f1}); rec.shotBreadcrumb=path.basename(f1);
  // C) node-list 搜索无结果
  await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
  await page.fill('input[placeholder="搜索名称/型号"]','zzz-no-match'); await page.waitForTimeout(1800);
  rec.filteredEmpty = await page.evaluate(()=>{
    const q=s=>document.querySelector(s); const qa=s=>[...document.querySelectorAll(s)];
    const R=e=>{if(!e)return null;const r=e.getBoundingClientRect();return{w:Math.round(r.width),h:Math.round(r.height),y:Math.round(r.y)};};
    return { cards:qa('.collector-card').length, emptyState:qa('.empty-state').length, elEmpty:qa('.el-empty').length,
      emptyTitle:q('.empty-title')?.textContent?.trim(), emptyText:q('.el-empty__description')?.textContent?.trim()?.slice(0,80),
      quickActions:qa('.empty-quick-actions button').map(b=>(b.textContent||'').trim()),
      bodyText:(document.body.innerText||'').replace(/\s+/g,' ').match(/暂无[^ ]{0,20}/g),
      statsRow:qa('.stat-card, .stat-item').map(e=>(e.textContent||'').replace(/\s+/g,' ').trim().slice(0,24)),
      boxEmptyState:R(q('.empty-state')) };
  });
  const f2=path.join('/tmp/uiux-d2-probe',VP+'-'+theme+'-p6-node-filtered.png'); await page.screenshot({path:f2}); rec.shotFiltered=path.basename(f2);
  // D) device-configs 弹窗 footer
  await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2200);
  await page.locator('button:has-text("新建模板")').first().click().catch(()=>{}); await page.waitForTimeout(1500);
  rec.cfgDialog = await page.evaluate(()=>{
    const d=document.querySelector('.el-dialog'); if(!d) return {open:false};
    const f=d.querySelector('.el-dialog__footer'); const b=d.querySelector('.el-dialog__body');
    const r=f.getBoundingClientRect();
    return { open:true, vh:innerHeight, dialogH:Math.round(d.getBoundingClientRect().height),
      footerBottom:Math.round(r.bottom), footerTop:Math.round(r.top), footerInViewport:r.bottom<=innerHeight && r.top>=0,
      bodyScrollH:b.scrollHeight, bodyClientH:b.clientHeight, bodyOverflowY:getComputedStyle(b).overflowY,
      dialogOverflowY:getComputedStyle(d).overflowY, footerPos:getComputedStyle(f).position,
      canScrollDialog: d.scrollHeight>d.clientHeight };
  });
  await ctx.close();
  out[VP+'-'+theme]=rec;
}
await run('light',1440,900,'desktop-1440');
await run('light',390,844,'mobile-390');
await run('light',360,800,'mobile-360');
await browser.close();
fs.writeFileSync('/tmp/uiux-d2-probe/p6.json', JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1).slice(0,6000));
