
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d1', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const out={};
for (const vp of [[360,800],[390,844]]) {
  const ctx=await browser.newContext({viewport:{width:vp[0],height:vp[1]},locale:'zh-CN',deviceScaleFactor:1,hasTouch:true,isMobile:true});
  const page=await ctx.newPage();
  // 大数值 KPI + 有实体但无数据
  await page.route('**/api/v1/overview', r=>r.fulfill({status:200,contentType:'application/json',
    body:JSON.stringify({code:200,message:'ok',data:{nodes:{total:1284567,online:999999,offline:284568},edge_devices:{total:98765,online:12345,offline:86420},latest_data:[]}})}));
  await page.route('**/api/v1/nodes/status-history*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
  out['vp'+vp[0]] = await page.evaluate(()=>{
    const vals=[...document.querySelectorAll('.dashboard-stats .stat-value')].map(e=>({
      t:e.textContent.trim(), sw:e.scrollWidth, cw:e.clientWidth, trunc:e.scrollWidth>e.clientWidth+1,
      textOverflow:getComputedStyle(e).textOverflow, boxW:Math.round(e.getBoundingClientRect().width)}));
    const labels=[...document.querySelectorAll('.dashboard-stats .stat-label')].map(e=>({
      t:e.textContent.trim(), sw:e.scrollWidth, cw:e.clientWidth, sh:e.scrollHeight, ch:e.clientHeight,
      trunc:e.scrollWidth>e.clientWidth+1, clippedH:e.scrollHeight>e.clientHeight+1,
      box:Math.round(e.getBoundingClientRect().width)+'x'+Math.round(e.getBoundingClientRect().height)}));
    const alert=[...document.querySelectorAll('.alert-item')].map(e=>({t:e.textContent.replace(/\s+/g,' ').trim(),
      sw:e.scrollWidth,cw:e.clientWidth}));
    return {vals,labels,alert,
      emptyTrend:(document.querySelector('.empty-state')||{}).textContent?.replace(/\s+/g,' ').trim(),
      emptyAll:[...document.querySelectorAll('.empty-state,.el-empty')].map(e=>e.textContent.replace(/\s+/g,' ').trim().slice(0,60)),
      kpiTotals:document.querySelectorAll('.dashboard-stats .stat-card').length};
  });
  await page.screenshot({path:OUT+'/dashboard-bignum-'+vp[0]+'.png'});
  await ctx.close();
}
fs.writeFileSync(OUT+'/chart-facts7.json',JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1));
await browser.close();
