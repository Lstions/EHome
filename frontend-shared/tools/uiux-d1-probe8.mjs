
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d1', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const ctx=await browser.newContext({viewport:{width:1440,height:900},locale:'zh-CN',deviceScaleFactor:1});
const page=await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
const out={};

// A) /api/v1/overview 失败（500）→ KPI 是否伪造成 0
await page.route('**/api/v1/overview', r=>r.fulfill({status:500,contentType:'application/json',body:JSON.stringify({code:500,message:'boom'})}));
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
out.overviewFail = await page.evaluate(()=>({
  kpi:[...document.querySelectorAll('.dashboard-stats .stat-card')].map(c=>({v:(c.querySelector('.stat-value')||{}).textContent.trim(),l:(c.querySelector('.stat-label')||{}).textContent.trim()})),
  alertCard: !!document.querySelector('.alert-summary'), alertText:(document.querySelector('.alert-summary')||{}).textContent?.replace(/\s+/g,' ').trim().slice(0,60),
  skeletons:document.querySelectorAll('.dashboard-stats [class*="skeleton"], .dashboard-stats .el-skeleton').length,
  empties:[...document.querySelectorAll('.empty-state, .el-empty')].map(e=>e.textContent.trim().replace(/\s+/g,' ').slice(0,44)),
  elResult:document.querySelectorAll('.el-result').length, errorBoundary:document.querySelectorAll('[class*="error"]').length,
  messages:[...document.querySelectorAll('.el-message')].map(e=>e.textContent.trim()),
  trendHeader:(document.querySelector('.trend-card-header')||{}).textContent?.replace(/\s+/g,' ').trim(),
}));
await page.screenshot({path:OUT+'/dashboard-overview-fail.png', fullPage:true});
await page.unroute('**/api/v1/overview').catch(()=>{});

// B) overview 成功但 latest_data 为空 → 空态数量
await page.route('**/api/v1/overview', r=>r.fulfill({status:200,contentType:'application/json',
  body:JSON.stringify({code:200,message:'ok',data:{nodes:{total:2,online:0,offline:2},edge_devices:{total:3,online:0,offline:3},latest_data:[]}})}));
await page.route('**/api/v1/nodes/status-history*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
out.emptyAll = await page.evaluate(()=>({
  kpi:[...document.querySelectorAll('.dashboard-stats .stat-card')].map(c=>({v:(c.querySelector('.stat-value')||{}).textContent.trim(),l:(c.querySelector('.stat-label')||{}).textContent.trim()})),
  empties:[...document.querySelectorAll('.empty-state, .el-empty')].map(e=>e.textContent.trim().replace(/\s+/g,' ').slice(0,50)),
  emptyCount:document.querySelectorAll('.empty-state, .el-empty').length,
  alertOk:(document.querySelector('.alert-ok')||{}).textContent?.trim(),
  alertTag:(document.querySelector('.alert-summary .el-tag')||{}).textContent?.trim(),
  alertIconColor:(()=>{const i=document.querySelector('.alert-summary .el-icon');return i?getComputedStyle(i).color:null})(),
}));
await page.screenshot({path:OUT+'/dashboard-empty.png', fullPage:true});

// C) 图表 tooltip 内联色：亮色主题的 tooltip 在暗色主题下是否已随主题变化（已知从探针1）；这里测 token 一致性与单位降级
// D) cancel 请求（离线/超时）→ data panel 的表现
await page.unroute('**/api/v1/overview').catch(()=>{});
await page.route('**/api/v1/edge-devices/*/data*', r=>r.abort('failed'));
await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(600);
const o=await page.$$('.el-select-dropdown:visible .el-select-dropdown__item'); await o[0].click(); await page.waitForTimeout(300); await page.keyboard.press('Escape');
await page.click('button:has-text("查询")'); await page.waitForTimeout(2500);
out.dataFail = await page.evaluate(()=>({
  messages:[...document.querySelectorAll('.el-message')].map(e=>({t:e.textContent.trim(),cls:e.className})),
  empties:[...document.querySelectorAll('.empty-state, .el-empty')].map(e=>e.textContent.trim().replace(/\s+/g,' ').slice(0,50)),
  hasRetry:[...document.querySelectorAll('button')].some(b=>/重试|重新加载/.test(b.textContent)),
  statPresent:document.querySelectorAll('.data-stats').length,
  tablePresent:document.querySelectorAll('.el-table').length,
}));
await page.screenshot({path:OUT+'/data-fail.png', fullPage:true});
fs.writeFileSync(OUT+'/chart-facts6.json',JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1));
await browser.close();
