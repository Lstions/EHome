
import { chromium } from '@playwright/test';
import fs from 'node:fs'; import path from 'node:path';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d1', CHROME='/snap/bin/chromium';
const NOW=Date.now();
const mk=(vals)=>vals.map((v,i)=>({id:i,device_id:7053,sensor_name:'x',value:v,unit:'',timestamp:new Date(NOW-(vals.length-i)*30000).toISOString(),created_at:new Date(NOW-(vals.length-i)*30000).toISOString()}));
const wob=(n,b,a)=>Array.from({length:n},(_,i)=>+(b+a*Math.sin(i/5)).toFixed(3));
const CATS=[['temperature',wob(120,22.5,3.4),'C']]; for(let i=1;i<=16;i++) CATS.push(['cell_voltage_'+i,wob(120,3.30+i*0.002,.02),'V']);
const batch={code:200,message:'ok',data:CATS.map(([c,v,u])=>({category:c,data:mk(v).map(p=>({...p,sensor_name:c,unit:u}))}))};
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const out={};
for (const theme of ['light','dark']) {
  const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1,colorScheme:theme});
  const page=await ctx.newPage();
  await page.route('**/api/v1/unified-data/historical-batch*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(batch)}));
  await page.route(u=>u.pathname.endsWith('/api/v1/unified-data/historical'),r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
  await page.route('**/api/v1/edge-devices/*/data*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:{items:Array.from({length:20},(_,i)=>({collected_at:new Date(NOW-i*60000).toISOString(),data:{total_voltage:48,cell_voltage_1:3.3},error_code:0})),total:137,page:1,page_size:20}})}));
  await page.route('**/api/v1/unified-data/categories*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[{code:'cell_voltage_1',unit:'V'}]})}));
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
  const t={};
  // ---- dashboard @390：表格包裹、空态重复、alert-item 可达性 ----
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
  t.dashboard390 = await page.evaluate(()=>{
    const bw=document.querySelector('.el-table__body-wrapper');
    return {
      mobileTableWrapper:document.querySelectorAll('.mobile-table-wrapper').length,
      mobileTableHint:document.querySelectorAll('.mobile-table-hint').length,
      tableCount:document.querySelectorAll('.el-table').length,
      bodyWrapper: bw?{sw:bw.scrollWidth,cw:bw.clientWidth,scrollable:bw.scrollWidth>bw.clientWidth+2,scrollbarH:bw.offsetHeight-bw.clientHeight}:null,
      emptyStates:[...document.querySelectorAll('.empty-state, .el-empty')].map(e=>e.textContent.trim().replace(/\s+/g,' ').slice(0,50)),
      alertItem: (()=>{const a=document.querySelector('.alert-item');return a?{tag:a.tagName,role:a.getAttribute('role'),tabindex:a.getAttribute('tabindex'),tabIndex:a.tabIndex,aria:a.getAttribute('aria-label'),text:a.textContent.replace(/\s+/g,' ').trim()}:null})(),
      alertOk: (()=>{const a=document.querySelector('.alert-ok');return a?a.textContent.replace(/\s+/g,' ').trim():null})(),
      focusableSeq:[...document.querySelectorAll('a[href],button,input,[tabindex]:not([tabindex="-1"])')].map(e=>(e.getAttribute('aria-label')||e.textContent||e.tagName).trim().replace(/\s+/g,' ').slice(0,18)).slice(0,14),
      statLabelClr: (()=>{const e=document.querySelector('.dashboard-stats .stat-label');return e?getComputedStyle(e).color:null})(),
      cardBg: (()=>{const e=document.querySelector('.dashboard-stats .el-card');return e?getComputedStyle(e).backgroundColor:null})(),
      alertItemBg:(()=>{const e=document.querySelector('.alert-item');return e?getComputedStyle(e).backgroundColor:null})(),
    };});
  await page.screenshot({path:path.join(OUT,'dashboard-390-'+theme+'.png')});
  // ---- data panel：切设备后分页是否保持 ----
  await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
  await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(600);
  await page.click('.el-select-dropdown__item'); await page.waitForTimeout(300); await page.keyboard.press('Escape');
  await page.click('button:has-text("查询")'); await page.waitForTimeout(3000);
  const pag = await page.evaluate(()=>({total:(document.querySelector('.el-pagination__total')||{}).textContent, current:(document.querySelector('.el-pager .is-active')||{}).textContent, rows:document.querySelectorAll('.el-table__row').length}));
  // 跳到第 3 页
  await page.click('.el-pager li:nth-child(3)').catch(()=>{}); await page.waitForTimeout(2500);
  const p3 = await page.evaluate(()=>({current:(document.querySelector('.el-pager .is-active')||{}).textContent, rows:document.querySelectorAll('.el-table__row').length, firstTime:(document.querySelector('.el-table__row td')||{}).textContent}));
  // 更换时间范围（改变查询范围）后点查询
  await page.click('.el-form-item:has-text("时间范围") .el-select'); await page.waitForTimeout(600);
  const opts = await page.$$('.el-select-dropdown:visible .el-select-dropdown__item');
  await opts[3].click(); await page.waitForTimeout(400); await page.keyboard.press('Escape');
  await page.click('button:has-text("查询")'); await page.waitForTimeout(3000);
  const afterFilter = await page.evaluate(()=>({current:(document.querySelector('.el-pager .is-active')||{}).textContent, timeLabel:(document.querySelector('.el-form-item:nth-child(2) .el-select__selected-item')||{}).textContent, rows:document.querySelectorAll('.el-table__row').length}));
  t.dataPaging={pag,p3,afterFilter};
  t.data390 = await page.evaluate(()=>({mobileTableWrapper:document.querySelectorAll('.mobile-table-wrapper').length, mobileTableHint:document.querySelectorAll('.mobile-table-hint').length, hint:(document.querySelector('.mobile-table-hint')||{}).textContent, statValues:[...document.querySelectorAll('.data-stats .stat-value')].map(e=>e.textContent.trim()), statLabels:[...document.querySelectorAll('.data-stats .stat-label')].map(e=>e.textContent.trim()), statUnitFont:(()=>{const u=document.querySelector('.data-stats .stat-unit');return u?{font:getComputedStyle(u).fontSize,color:getComputedStyle(u).color}:'no unit rendered'})()}));
  await page.screenshot({path:path.join(OUT,'data-390-'+theme+'.png')});
  out[theme]=t; await ctx.close();
}
await browser.close(); fs.writeFileSync(path.join(OUT,'chart-facts3.json'),JSON.stringify(out,null,2)); console.log('ok');
