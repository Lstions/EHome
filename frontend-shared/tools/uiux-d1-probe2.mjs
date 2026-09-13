
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE='http://127.0.0.1:8082', OUT=process.env.UIUX_OUT||'/tmp/uiux-d1', CHROME='/snap/bin/chromium';
fs.mkdirSync(OUT,{recursive:true});
const NOW=Date.now();
const mk=(vals)=>vals.map((v,i)=>({id:i,device_id:7053,sensor_name:'x',value:v,unit:'',timestamp:new Date(NOW-(vals.length-i)*30000).toISOString(),created_at:new Date(NOW-(vals.length-i)*30000).toISOString()}));
const wob=(n,b,a)=>Array.from({length:n},(_,i)=>+(b+a*Math.sin(i/5)).toFixed(3));
const CATS=[['temperature',wob(120,22.5,3.4),'C'],['total_voltage',wob(120,48,1.2),'V'],['cell_voltage_max',wob(120,3.35,.03),'V'],['cell_voltage_min',wob(120,3.28,.03),'V'],['current',wob(120,2,.4),'A']];
for(let i=1;i<=16;i++) CATS.push(['cell_voltage_'+i,wob(120,3.30+i*0.002,.02),'V']);
const batch={code:200,message:'ok',data:CATS.map(([c,v,u])=>({category:c,data:mk(v).map(p=>({...p,sensor_name:c,unit:u}))}))};

// 画布像素取色：canvas 渲染器的图元颜色是原样写入的，可据此判定网格线/坐标轴文字/系列色
const CANVAS_COLORS = () => {
  const cv = document.querySelector('.line-chart canvas'); if(!cv) return {err:'no canvas'};
  const ctx = cv.getContext('2d', { willReadFrequently: true }); if(!ctx) return {err:'no 2d ctx'};
  const d = ctx.getImageData(0,0,cv.width,cv.height).data;
  const m = new Map();
  for (let i=0;i<d.length;i+=4){ const a=d[i+3]; if(a<250) continue;
    const k=(d[i]<<16)|(d[i+1]<<8)|d[i+2]; m.set(k,(m.get(k)||0)+1); }
  const top=[...m.entries()].sort((a,b)=>b[1]-a[1]).slice(0,14).map(([k,c])=>({hex:'#'+k.toString(16).padStart(6,'0'),n:c}));
  return { w:cv.width, h:cv.height, distinct:m.size, top };
};

const browser = await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const rep={};
for (const theme of ['light','dark']) {
  const ctx = await browser.newContext({viewport:{width:1440,height:900},locale:'zh-CN',deviceScaleFactor:1,colorScheme:theme});
  const page = await ctx.newPage();
  await page.route('**/api/v1/unified-data/historical-batch*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify(batch)}));
  await page.route(u=>u.pathname.endsWith('/api/v1/unified-data/historical'), r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
  await page.route('**/api/v1/edge-devices/*/data*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:{items:[],total:0}})}));
  await page.route('**/api/v1/unified-data/categories*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[{code:'total_voltage',unit:'V'}]})}));

  // 首次加载：延迟指标接口 5s，观察 KPI 在数据到达前显示了什么
  await page.route('**/api/v1/metrics/summary*', async r => { await new Promise(s=>setTimeout(s,5000)); r.continue(); });

  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")');
  await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{});
  await page.waitForTimeout(1500);

  const t={};
  // ---- 1) monitor 首屏（接口被延迟 5s）：KPI 显示什么 ----
  await page.goto(BASE+'/monitor',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(1200);   // 接口尚未返回
  t.monitor_firstload = await page.evaluate(()=>({
    skeleton: document.querySelectorAll('.el-skeleton, [class*="skeleton"]').length,
    loadingMask: document.querySelectorAll('.el-loading-mask').length,
    statValues: [...document.querySelectorAll('.stat-cards .stat-value')].map(e=>e.textContent.trim()),
    statLabels: [...document.querySelectorAll('.stat-cards .stat-label')].map(e=>e.textContent.trim()),
    controlMetrics: [...document.querySelectorAll('.control-metric')].map(e=>e.textContent.trim().replace(/\s+/g,' ')),
    lastUpdate: (document.querySelector('.footer-info')||{}).textContent,
    alertPresent: !!document.querySelector('.control-alert'),
    descriptions: [...document.querySelectorAll('.el-descriptions__label')].length ? [...document.querySelectorAll('.el-descriptions')].map(d=>d.textContent.replace(/\s+/g,' ').trim().slice(0,80)) : [],
  }));
  await page.screenshot({path:path.join(OUT,'monitor-firstload-'+theme+'.png')});
  // 等真实数据到达
  await page.waitForTimeout(6000);
  t.monitor_loaded = await page.evaluate(()=>({
    statValues:[...document.querySelectorAll('.stat-cards .stat-value')].map(e=>e.textContent.trim()),
    alertPresent: !!document.querySelector('.control-alert'),
    alertText: (document.querySelector('.control-alert')||{}).textContent,
    alertType: (()=>{const a=document.querySelector('.control-alert');return a?a.className:null})(),
  }));
  // 进度条颜色（el-progress :color 传的是 THEME_COLORS 原始 hex）
  t.progress = await page.evaluate(()=>[...document.querySelectorAll('.el-progress-bar__inner')].map(e=>({
    bg:getComputedStyle(e).backgroundColor, inline:e.getAttribute('style'),
    label:(e.closest('.status-item')||{}).textContent?.replace(/\s+/g,' ').trim() })));
  t.tokens = await page.evaluate(()=>{const s=getComputedStyle(document.documentElement);
    const o={}; ['--color-success','--el-color-success','--color-danger','--el-color-danger','--color-warning','--el-color-warning','--el-color-info'].forEach(k=>o[k]=s.getPropertyValue(k).trim()); return o;});
  // DOM 节点随时间增长（10s 自动刷新）
  const growth=[];
  for (let i=0;i<4;i++){ growth.push(await page.evaluate(()=>document.querySelectorAll('body *').length)); await page.waitForTimeout(9000); }
  growth.push(await page.evaluate(()=>document.querySelectorAll('body *').length));
  t.monitor_domGrowth = growth;

  // ---- 1b) monitor 接口成功但全 0 ----
  await page.route('**/api/v1/metrics/summary*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,data:{timestamp:Date.now(),http:{requests_total:0,requests_in_flight:0},mqtt:{messages_received:0,messages_sent:0,connection_errors:0},device:{online:0,offline:0},node:{online:0,offline:0},data:{points_collected:0,points_stored:0},ota:{upgrades_total:0},websocket:{connections_active:0,messages_total:0},control:{operations_total:0,active:0,queued:0,succeeded:0,failed:0,unknown:0,unresolved_unknown:0,cancelled:0,outbox_pending:0,outbox_leased:0,capability_stale_nodes:0,audit_write_failures:0}}})}));
  await page.goto(BASE+'/monitor',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(2000);
  t.monitor_zero = await page.evaluate(()=>({
    statValues:[...document.querySelectorAll('.stat-cards .stat-value')].map(e=>({t:e.textContent.trim(),c:getComputedStyle(e).color,html:e.innerHTML.slice(0,80)})),
    controlMetrics:[...document.querySelectorAll('.control-metric')].map(e=>e.textContent.trim().replace(/\s+/g,' ')),
    alertPresent:!!document.querySelector('.control-alert'),
    lastUpdate:(document.querySelector('.footer-info')||{}).textContent,
    progress:[...document.querySelectorAll('.el-progress-bar__inner')].map(e=>({bg:getComputedStyle(e).backgroundColor,inline:e.getAttribute('style'),label:(e.closest('.status-item')||{}).textContent?.replace(/\s+/g,' ').trim()})),
  }));
  await page.screenshot({path:path.join(OUT,'monitor-zero-'+theme+'.png')});
  await page.unroute('**/api/v1/metrics/summary*').catch(()=>{});

  // ---- 2) 图表像素取色 ----
  await page.unroute('**/api/v1/metrics/summary*').catch(()=>{});
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(3000);
  t.dashboardCanvas = await page.evaluate(CANVAS_COLORS);
  t.dashboardZero = await page.evaluate(()=>({
    trendHeader: (document.querySelector('.trend-card-header')||{}).textContent?.replace(/\s+/g,' ').trim(),
    statValues: [...document.querySelectorAll('.dashboard-stats .stat-value')].map(e=>e.textContent.trim()),
    unitEls: document.querySelectorAll('.dashboard-stats .stat-unit').length,
    emptyText: (document.querySelector('.empty-state')||{}).textContent?.replace(/\s+/g,' ').trim().slice(0,90),
  }));
  await page.goto(BASE+'/edge-device/7053',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(3500);
  t.detailCanvas = await page.evaluate(CANVAS_COLORS);
  t.detailCanvas2 = await page.evaluate(()=>{const cvs=[...document.querySelectorAll('.line-chart canvas')];if(cvs.length<2)return {err:'only '+cvs.length};const cv=cvs[1];const c2=cv.getContext('2d',{willReadFrequently:true});const d=c2.getImageData(0,0,cv.width,cv.height).data;const m=new Map();for(let i=0;i<d.length;i+=4){if(d[i+3]<250)continue;const k=(d[i]<<16)|(d[i+1]<<8)|d[i+2];m.set(k,(m.get(k)||0)+1);}return {w:cv.width,h:cv.height,distinct:m.size,top:[...m.entries()].sort((a,b)=>b[1]-a[1]).slice(0,10).map(([k,c])=>({hex:'#'+k.toString(16).padStart(6,'0'),n:c}))};});
  t.detailLegend = await page.evaluate(()=>{
    const cards=[...document.querySelectorAll('.el-card')];
    const t2=cards.find(c=>c.textContent.includes('电芯电压历史趋势'));
    return t2? {title:(t2.querySelector('.el-card__header')||{}).textContent?.replace(/\s+/g,' ').trim(),
      subTitles:[...t2.querySelectorAll('.chart-sub-title')].map(e=>e.textContent.trim()),
      checkboxes:t2.querySelectorAll('.el-checkbox').length,
      checked:t2.querySelectorAll('.el-checkbox.is-checked').length}:null;});
  // 单位渲染样式（§4.2.4）
  await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'});
  await page.waitForTimeout(1500);
  try{
    await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(600);
    await page.click('.el-select-dropdown__item'); await page.waitForTimeout(400); await page.keyboard.press('Escape');
    await page.click('button:has-text("查询")'); await page.waitForTimeout(3500);
    t.dataUnits = await page.evaluate(()=>[...document.querySelectorAll('.data-stats .stat-value')].map(e=>({
      text:e.textContent.trim(), unit:e.querySelector('.stat-unit')?getComputedStyle(e.querySelector('.stat-unit')).fontSize:null,
      unitColor:e.querySelector('.stat-unit')?getComputedStyle(e.querySelector('.stat-unit')).color:null,
      valueColor:getComputedStyle(e).color, valueFont:getComputedStyle(e).fontSize})));
    t.dataLabels = await page.evaluate(()=>[...document.querySelectorAll('.data-stats .stat-label')].map(e=>e.textContent.trim()));
    t.dataCanvas = await page.evaluate(CANVAS_COLORS);
    t.dataTooltipMax = await page.evaluate(()=>{const el=document.querySelector('.line-chart');return el?el.getBoundingClientRect().width:null;});
    t.dataPagination = await page.evaluate(()=>[...document.querySelectorAll('.el-pagination')].map(e=>e.textContent.replace(/\s+/g,' ').trim()));
    t.dataRangeText = await page.evaluate(()=>{const c=[...document.querySelectorAll('.el-card')].find(c=>c.textContent.includes('历史数据趋势'));return c?c.textContent.replace(/\s+/g,' ').trim().slice(0,120):null;});
  }catch(e){ t.dataErr=String(e).slice(0,150); }
  rep[theme]=t;
  await ctx.close();
}
await browser.close();
fs.writeFileSync(path.join(OUT,'chart-facts2.json'),JSON.stringify(rep,null,2));
console.log('ok');