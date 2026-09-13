
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d1', CHROME='/snap/bin/chromium';
const NOW=Date.now();
const mk=(vals)=>vals.map((v,i)=>({id:i,device_id:1,sensor_name:'x',value:v,unit:'',timestamp:new Date(NOW-(vals.length-i)*30000).toISOString(),created_at:new Date(NOW-(vals.length-i)*30000).toISOString()}));
const batch={code:200,message:'ok',data:[{category:'temperature',data:mk(Array.from({length:120},(_,i)=>22+3*Math.sin(i/5))) }]};
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1,hasTouch:true,isMobile:true});
const page=await ctx.newPage();
const reqs=[];
page.on('request', r=>{ if(r.url().includes('/api/v1/edge-devices/')&&r.url().includes('/data?')) reqs.push(decodeURIComponent(r.url().split('/api/v1')[1])); });
await page.route('**/api/v1/unified-data/historical-batch*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(batch)}));
await page.route(u=>u.pathname.endsWith('/api/v1/unified-data/historical'),r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
await page.route('**/api/v1/edge-devices/*/data*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:{items:Array.from({length:20},(_,i)=>({collected_at:new Date(NOW-i*60000).toISOString(),data:{temperature:22},error_code:0})),total:137,page:1,page_size:20}})}));
await page.route('**/api/v1/unified-data/categories*',r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[{code:'temperature',unit:'C'}]})}));
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
const out={};

// ===== A. DataPanel 分页是否随查询范围重置（读真实请求参数 + 选中项文本）=====
await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
const selText = () => page.evaluate(()=>{
  const items=[...document.querySelectorAll('.el-form-item')];
  const pick=i=>{const f=items[i];if(!f)return null;const t=f.querySelector('.el-select__selected-item, .el-select__placeholder');return t?t.textContent.trim():f.textContent.trim().slice(0,30)};
  return {device:pick(0), range:pick(1), page:(document.querySelector('.el-pager .is-active')||{}).textContent.trim(), total:(document.querySelector('.el-pagination__total')||{}).textContent};});
const openSel = async (i) => { const f=(await page.$$('.el-form-item'))[i]; await f.$eval('.el-select', e=>e.scrollIntoView({block:'center'})); await page.waitForTimeout(300);
  const s=await f.$('.el-select'); await s.click(); await page.waitForTimeout(700); };
const choose = async (idx) => { const o=await page.$$('.el-select-dropdown:visible .el-select-dropdown__item'); await o[idx].click(); await page.waitForTimeout(400); await page.keyboard.press('Escape'); await page.waitForTimeout(300); };
await openSel(0); await choose(0); await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
const a1=await selText(); reqs.length=0;
await page.click('.el-pager li:nth-child(3)'); await page.waitForTimeout(2500);
const a2=await selText(); const reqPage3=reqs.slice(-1)[0];
// 改时间范围 → 再查询
await openSel(1); await choose(3);
const a3=await selText();
await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
const a4=await selText(); const reqAfterRange=reqs.slice(-1)[0];
// 改设备 → 再查询
await openSel(0); await choose(1);
const a5=await selText();
await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
const a6=await selText(); const reqAfterDevice=reqs.slice(-1)[0];
out.pagination={a1,a2,reqPage3,a3,a4,reqAfterRange,a5,a6,reqAfterDevice};

// ===== B. 骨架屏 vs 内容几何（dashboard 首屏）=====
await page.setViewportSize({width:1440,height:900});
await page.route('**/api/v1/overview', async r=>{ await new Promise(s=>setTimeout(s,4000)); r.continue(); });
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1200);
out.skeletonDesktop = await page.evaluate(()=>{
  const r=e=>{const b=e.getBoundingClientRect();return {w:Math.round(b.width),h:Math.round(b.height),x:Math.round(b.x),y:Math.round(b.y)}};
  const sk=[...document.querySelectorAll('.dashboard-stats [class*="skeleton"], .dashboard-stats .el-skeleton, .dashboard-stats > *')].map(r);
  return {count:document.querySelectorAll('.dashboard-stats > *').length, rects:sk, statValues:document.querySelectorAll('.dashboard-stats .stat-value').length,
    grids:document.querySelectorAll('.dashboard-stats').length};});
await page.waitForTimeout(5000);
out.loadedDesktop = await page.evaluate(()=>{
  const r=e=>{const b=e.getBoundingClientRect();return {w:Math.round(b.width),h:Math.round(b.height),x:Math.round(b.x),y:Math.round(b.y)}};
  return {count:document.querySelectorAll('.dashboard-stats > *').length, rects:[...document.querySelectorAll('.dashboard-stats > *')].map(r),
    statValues:document.querySelectorAll('.dashboard-stats .stat-value').length,
    cards:[...document.querySelectorAll('.dashboard-stats .el-card')].map(r)};});
await page.setViewportSize({width:390,height:844}); await page.waitForTimeout(600);

// ===== C. dashboard 小号 select 的实际字号（可见文本 + input）=====
await page.unroute('**/api/v1/overview').catch(()=>{});
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
out.smallSelect = await page.evaluate(()=>{
  const s=document.querySelector('.trend-category-select');
  if(!s) return {err:'no select'};
  const g=e=>{const c=getComputedStyle(e);return {font:c.fontSize,color:c.color,h:Math.round(e.getBoundingClientRect().height),w:Math.round(e.getBoundingClientRect().width),tag:e.tagName,cls:(e.className||'').toString().slice(0,30)}};
  const inner=s.querySelector('.el-select__wrapper'), inp=s.querySelector('input'),
        sel=s.querySelector('.el-select__selected-item'), ph=s.querySelector('.el-select__placeholder');
  return {wrapper:inner?g(inner):null, input:inp?{...g(inp),readonly:inp.readOnly,type:inp.type,tabIndex:inp.tabIndex}:null,
    selectedItem:sel?g(sel):null, placeholder:ph?g(ph):null, text:s.textContent.trim()};});

// ===== D. 过渡/动效事实 =====
out.transitions = await page.evaluate(()=>[...document.querySelectorAll('.stat-card, .stat-card *')].slice(0,3).map(e=>({cls:(e.className||'').toString().slice(0,24),transition:getComputedStyle(e).transition,anim:getComputedStyle(e).animationDuration})));
// hover 后 transform 实测
const card = await page.$('.dashboard-stats .el-card');
if (card) { const b=await card.boundingBox(); await page.mouse.move(b.x+b.width/2,b.y+b.height/2); await page.waitForTimeout(700);
  out.hoverTransform = await page.evaluate(()=>getComputedStyle(document.querySelector('.dashboard-stats .el-card')).transform);
  await page.mouse.move(5,5); await page.waitForTimeout(500); }
out.hoverTransformAfter = await page.evaluate(()=>getComputedStyle(document.querySelector('.dashboard-stats .el-card')).transform);

// ===== E. 单位渲染（data panel，注入有单位的类别）=====
await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(600);
const o=await page.$$('.el-select-dropdown:visible .el-select-dropdown__item'); await o[0].click(); await page.waitForTimeout(300); await page.keyboard.press('Escape');
await page.click('button:has-text("查询")'); await page.waitForTimeout(3000);
out.dataStats = await page.evaluate(()=>({
  values:[...document.querySelectorAll('.data-stats .stat-value')].map(e=>({t:e.textContent.trim(),font:getComputedStyle(e).fontSize,color:getComputedStyle(e).color,
    unit:e.querySelector('.stat-unit')?{font:getComputedStyle(e.querySelector('.stat-unit')).fontSize,color:getComputedStyle(e.querySelector('.stat-unit')).color,text:e.querySelector('.stat-unit').textContent}:null})),
  labels:[...document.querySelectorAll('.data-stats .stat-label')].map(e=>e.textContent.trim()),
  count:document.querySelectorAll('.data-stats .el-card').length,
  gridCols:getComputedStyle(document.querySelector('.data-stats')).gridTemplateColumns}));
fs.writeFileSync(OUT+'/chart-facts5.json',JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1));
await browser.close();
