
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d1', CHROME='/snap/bin/chromium';
const NOW=Date.now();
const mk=(vals)=>vals.map((v,i)=>({id:i,device_id:1,sensor_name:'x',value:v,unit:'',timestamp:new Date(NOW-(vals.length-i)*30000).toISOString(),created_at:new Date(NOW-(vals.length-i)*30000).toISOString()}));
const CATS=[['temperature',Array.from({length:120},(_,i)=>+(22+3*Math.sin(i/5)).toFixed(2)),'C']];
const batch={code:200,message:'ok',data:CATS.map(([c,v,u])=>({category:c,data:mk(v).map(p=>({...p,sensor_name:c,unit:u}))}))};
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const out={};
for (const theme of ['light','dark']) {
  const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1,hasTouch:true,isMobile:true,colorScheme:theme});
  const page=await ctx.newPage();
  await page.route('**/api/v1/unified-data/historical-batch*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(batch)}));
  await page.route(u=>u.pathname.endsWith('/api/v1/unified-data/historical'),r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
  await page.route('**/api/v1/edge-devices/*/data*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:{items:Array.from({length:20},(_,i)=>({collected_at:new Date(NOW-i*60000).toISOString(),data:{temperature:22},error_code:0})),total:137,page:1,page_size:20}})}));
  await page.route('**/api/v1/unified-data/categories*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[{code:'temperature',unit:'C'}]})}));
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
  const t={};

  // ===== D6: 触控目标 & 输入字号（390 宽）=====
  const TOUCH = () => {
    const rect=e=>{const r=e.getBoundingClientRect();return {w:Math.round(r.width),h:Math.round(r.height)}};
    const items=[];
    document.querySelectorAll('button,.el-button,.el-switch,.el-checkbox,[role="button"],.el-radio-button__inner,.el-select,.el-input__inner,a.el-link').forEach(e=>{
      const r=e.getBoundingClientRect(); if(r.width<=0||r.height<=0) return;
      items.push({tag:e.tagName.toLowerCase(),cls:(e.className||'').toString().split(' ').slice(0,2).join('.'),
        label:(e.getAttribute('aria-label')||e.textContent||'').trim().replace(/\s+/g,' ').slice(0,20),
        w:Math.round(r.width),h:Math.round(r.height), below36:r.width<36||r.height<36, below44:r.width<44||r.height<44});
    });
    const inputs=[...document.querySelectorAll('.el-input__inner, textarea, input')].map(e=>({cls:(e.className||'').toString().split(' ')[0],font:getComputedStyle(e).fontSize,h:Math.round(e.getBoundingClientRect().height)}));
    return {items: items.filter(i=>i.below44), inputFonts:[...new Map(inputs.map(i=>[i.cls+'|'+i.font,i])).values()]};
  };
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
  t.dashboardTouch = await page.evaluate(TOUCH);
  await page.goto(BASE+'/monitor',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
  t.monitorTouch = await page.evaluate(TOUCH);
  await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2000);
  t.dataTouch = await page.evaluate(TOUCH);

  // ===== D5: 分页重置（切换设备 / 切换时间范围）=====
  await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
  const pickDevice = async (idx) => { await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(600);
    const opts=await page.$$('.el-select-dropdown:visible .el-select-dropdown__item'); await opts[idx].click(); await page.waitForTimeout(300); await page.keyboard.press('Escape'); };
  const pickRange = async (idx) => { await page.click('.el-form-item:has-text("时间范围") .el-select'); await page.waitForTimeout(600);
    const opts=await page.$$('.el-select-dropdown:visible .el-select-dropdown__item'); await opts[idx].click(); await page.waitForTimeout(400); await page.keyboard.press('Escape'); };
  const state = () => page.evaluate(()=>({page:(document.querySelector('.el-pager .is-active')||{}).textContent, rows:document.querySelectorAll('.el-table__row').length, total:(document.querySelector('.el-pagination__total')||{}).textContent,
    dev:(document.querySelector('.el-form-item:nth-child(1) .el-select__placeholder, .el-form-item:nth-child(1) .el-select__selected-item')||{}).textContent,
    range:(document.querySelector('.el-form-item:nth-child(2) .el-select__placeholder, .el-form-item:nth-child(2) .el-select__selected-item')||{}).textContent}));
  await pickDevice(0); await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
  const s1=await state();
  await page.click('.el-pager li:nth-child(3)'); await page.waitForTimeout(2500);
  const s2=await state();
  await pickRange(3); const s2b=await state();
  await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
  const s3=await state();               // 换时间范围后
  await pickDevice(1); const s3b=await state();
  await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
  const s4=await state();               // 换设备后
  t.pagination={s1,s2,s2b,s3,s3b,s4};

  // ===== D6: 页头换行 @360 =====
  await page.setViewportSize({width:360,height:800});
  await page.goto(BASE+'/monitor',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2200);
  t.monitor360 = await page.evaluate(()=>{
    const h2=document.querySelector('.toolbar h2'); const tb=document.querySelector('.toolbar');
    const r=e=>{const b=e.getBoundingClientRect();return {w:Math.round(b.width),h:Math.round(b.height),x:Math.round(b.x),right:Math.round(b.right)}};
    const cs=getComputedStyle(h2);
    return {h2:r(h2), h2Font:cs.fontSize, h2Wrap:cs.wordBreak, h2Text:h2.textContent.trim(),
      toolbar:r(tb), toolbarFlex:getComputedStyle(tb).flexDirection,
      actions:r(document.querySelector('.toolbar-actions')),
      select:r(document.querySelector('.refresh-interval-select')),
      btn:r(document.querySelector('.toolbar-actions .el-button')),
      lines: Math.round(h2.getBoundingClientRect().height / parseFloat(cs.lineHeight||cs.fontSize)),
      docOverflow:document.documentElement.scrollWidth-document.documentElement.clientWidth};});
  await page.screenshot({path:OUT+'/monitor-360-'+theme+'.png'});
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
  t.dashboard360 = await page.evaluate(()=>{
    const r=e=>{const b=e.getBoundingClientRect();return {w:Math.round(b.width),h:Math.round(b.height),x:Math.round(b.x),right:Math.round(b.right)}};
    const ph=document.querySelector('[class*="page-header"]');
    return {ph:r(ph), phText:ph.textContent.trim(), docOverflow:document.documentElement.scrollWidth-document.documentElement.clientWidth,
      tableHint:document.querySelectorAll('.mobile-table-hint').length, wrapper:document.querySelectorAll('.mobile-table-wrapper').length};});
  await page.screenshot({path:OUT+'/dashboard-360-'+theme+'.png'});
  await page.setViewportSize({width:390,height:844});

  // ===== D2: 对比度（统计卡标签 / 数值 在亮暗下的实际色）=====
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
  t.contrast = await page.evaluate(()=>{
    const card=document.querySelector('.dashboard-stats .el-card');
    const bg=card?getComputedStyle(card).backgroundColor:null;
    const g=s=>{const e=document.querySelector(s);return e?getComputedStyle(e).color:null};
    return {cardBg:bg, value:g('.dashboard-stats .stat-value'), label:g('.dashboard-stats .stat-label'),
      alertValue:g('.alert-value'), alertLabel:g('.alert-label')};});

  // ===== D7: 可点击非原生容器 =====
  t.a11y = await page.evaluate(()=>{
    const bad=[];
    document.querySelectorAll('div,span,li,p').forEach(e=>{
      const hasClick=e.getAttribute('@click')||e.onclick||e.getAttribute('data-clickable');
      const cls=(e.className||'').toString();
      if(!/(alert-item|stat-card|control-metric|status-item|chart-sub-section|device-link)/.test(cls)) return;
      const r=e.getBoundingClientRect(); if(r.width<=0) return;
      bad.push({cls:cls.split(' ')[0],role:e.getAttribute('role'),tabindex:e.getAttribute('tabindex'),tabIndex:e.tabIndex,
        aria:e.getAttribute('aria-label'), cursor:getComputedStyle(e).cursor, text:e.textContent.trim().replace(/\s+/g,' ').slice(0,20)});
    });
    return bad;});
  t.dashboardAlerts = await page.evaluate(()=>[...document.querySelectorAll('.alert-item')].map(e=>({role:e.getAttribute('role'),tabindex:e.getAttribute('tabindex'),tabIndex:e.tabIndex,aria:e.getAttribute('aria-label'),cursor:getComputedStyle(e).cursor,text:e.textContent.replace(/\s+/g,' ').trim().slice(0,20)})));
  out[theme]=t; await ctx.close();
}
await browser.close(); fs.writeFileSync(OUT+'/chart-facts4.json',JSON.stringify(out,null,2)); console.log('ok');
