/**
 * D2 精确取证 5：节点详情 TAB / 配置模板弹窗窄屏 / 筛选空态 / 计数一致性。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs'; import path from 'node:path';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d2-probe';
const THEME=process.env.UIUX_THEME||'light', W=Number(process.env.UIUX_W||390), H=Number(process.env.UIUX_H||844), VP=process.env.UIUX_VPNAME||'mobile-390';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:THEME==='dark'?'dark':'light' });
const page = await ctx.newPage(); const errs=[]; page.on('pageerror',e=>errs.push(String(e).slice(0,150)));
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},THEME);
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
const rep={vp:VP,theme:THEME,steps:[],consoleErrors:errs};
const shot=async n=>{const f=path.join(OUT,VP+'-'+THEME+'-p5-'+n+'.png'); await page.screenshot({path:f}); return path.basename(f);};

// 1) /node/1 全部 TAB
await page.goto(BASE+'/node/1',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
rep.steps.push({step:'/node/1 TAB 栏', shot: await shot('node1-tabs'), tabs: await page.evaluate(()=>{
  const qa=s=>[...document.querySelectorAll(s)]; const R=e=>{const r=e.getBoundingClientRect();return{w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x)};};
  return qa('.tab-item').map(t=>({label:(t.textContent||'').trim(), active:t.classList.contains('active'), ...R(t), role:t.getAttribute('role'), tabindex:t.getAttribute('tabindex')}));
}), header: await page.evaluate(()=>{
  const q=s=>document.querySelector(s); const R=e=>{if(!e)return null;const r=e.getBoundingClientRect();return{w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y),right:Math.round(r.right)};};
  return { pageHeader:R(q('.page-header')), h1:R(q('h1.ph-title')), h1text:q('h1.ph-title')?.textContent, actions:R(q('.ph-actions')),
    actionBtns:[...document.querySelectorAll('.ph-actions .btn')].map(b=>({t:(b.textContent||'').trim(),disabled:b.disabled,...R(b)})),
    breadcrumb:R(q('.no-breadcrumb')), breadcrumbText:(q('.no-breadcrumb')||{}).textContent };
}), disabledReasons: await page.evaluate(()=>{
  const qa=s=>[...document.querySelectorAll(s)];
  return qa('button:disabled').map(b=>{ let tip=b.getAttribute('title'); let n=b.closest('.el-tooltip__trigger, span'); return {t:(b.textContent||'').trim(), title:tip, wrappedByTooltipTrigger:!!b.closest('[aria-describedby]')||!!(n&&n.tagName==='SPAN'&&n.previousElementSibling===null)};});
}) });

// 2) device-configs 新建模板弹窗（680px 固定宽 in 390 viewport）
await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2200);
await page.locator('button:has-text("新建模板")').first().click().catch(e=>errs.push('cfg:'+e.message)); await page.waitForTimeout(1400);
rep.steps.push({step:'device-configs 新建模板弹窗', shot: await shot('cfg-dialog'), dialog: await page.evaluate(()=>{
  const d=document.querySelector('.el-dialog'); if(!d) return {open:false};
  const R=e=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),w:Math.round(r.width),right:Math.round(r.right),h:Math.round(r.height),bottom:Math.round(r.bottom)};};
  const body=d.querySelector('.el-dialog__body');
  return {open:true, ...R(d), vw:innerWidth, docOverflow:document.documentElement.scrollWidth-document.documentElement.clientWidth,
    bodyScrollH:body.scrollHeight, bodyClientH:body.clientHeight, bodyOverflowY:getComputedStyle(body).overflowY,
    footerVisible: (()=>{const f=d.querySelector('.el-dialog__footer'); if(!f) return null; const r=f.getBoundingClientRect(); return {bottom:Math.round(r.bottom), inViewport: r.bottom<=innerHeight};})(),
    footerBtns:[...d.querySelectorAll('.el-dialog__footer button')].map(b=>({t:(b.textContent||'').trim(),...R(b)}))};
}) });

// 3) node-list 筛选空态
await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2300);
await page.fill('input[placeholder="搜索名称/型号"]','zzz-no-match').catch(()=>{}); await page.waitForTimeout(1600);
rep.steps.push({step:'node-list 搜索无结果', shot: await shot('node-filtered-empty'), empty: await page.evaluate(()=>{
  const q=s=>document.querySelector(s); const qa=s=>[...document.querySelectorAll(s)];
  const es=q('.el-empty'); return { emptyPresent:!!es, emptyText:q('.el-empty__description')?.textContent?.replace(/\s+/g,' ').trim(),
    quickActions:qa('.el-empty button').map(b=>(b.textContent||'').trim()), hasFilterTag:!!q('.active-filters'),
    paginationText:qa('.el-pagination').map(p=>(p.textContent||'').replace(/\s+/g,' ').trim().slice(0,60)) };
}) });

// 4) logical-device 空态与工具栏计数
await page.goto(BASE+'/logical-device',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
rep.steps.push({step:'logical-device 空态', shot: await shot('ld-empty'), facts: await page.evaluate(()=>{
  const q=s=>document.querySelector(s);
  return { emptyText:q('.el-empty__description')?.textContent?.replace(/\s+/g,' ').trim()?.slice(0,120),
    hint:q('.mobile-table-hint')?.textContent?.trim(), mergeBtn:(q('.filter-right button')||{}).textContent?.trim(),
    mergeDisabled:(q('.filter-right button')||{}).disabled, mergeTitle:(q('.filter-right button')||{}).getAttribute('title') };
}) });

await browser.close();
fs.writeFileSync(path.join(OUT,'p5-'+VP+'-'+THEME+'.json'), JSON.stringify(rep,null,2));
console.log('steps='+rep.steps.length+' errs='+errs.length);
