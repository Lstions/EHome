
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const out={};
for (const vp of [[360,800],[390,844]]) {
  const ctx=await browser.newContext({viewport:{width:vp[0],height:vp[1]},locale:'zh-CN',deviceScaleFactor:1,hasTouch:true,isMobile:true});
  const page=await ctx.newPage();
  await page.route('**/api/v1/overview', r=>r.fulfill({status:200,contentType:'application/json',
    body:JSON.stringify({code:200,message:'ok',data:{nodes:{total:1284567,online:999999,offline:284568},edge_devices:{total:98765,online:12345,offline:86420},latest_data:[]}})}));
  await page.route('**/api/v1/nodes/status-history*', r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({code:200,message:'ok',data:[]})}));
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
  await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
  // 用 Range 量文本真实宽度（不依赖 scrollWidth），并和 clientWidth 比
  out['vp'+vp[0]] = await page.evaluate(()=>{
    const res=[];
    document.querySelectorAll('.dashboard-stats .stat-value').forEach(e=>{
      const r=document.createRange(); r.selectNodeContents(e);
      const tw=r.getBoundingClientRect().width;
      const cw=e.clientWidth;
      // 用 canvas 度量纯文本宽度，独立于布局
      const cs=getComputedStyle(e);
      const cv=document.createElement('canvas').getContext('2d');
      cv.font=cs.fontWeight+' '+cs.fontSize+' '+cs.fontFamily;
      const measured=cv.measureText(e.textContent.trim()).width;
      res.push({text:e.textContent.trim(), rangeWidth:+tw.toFixed(1), clientWidth:cw,
        canvasTextWidth:+measured.toFixed(1), overflowPx:+(measured-cw).toFixed(1),
        truncated: measured > cw + 0.5, textOverflow:cs.textOverflow, overflow:cs.overflow, whiteSpace:cs.whiteSpace});
    });
    return res;
  });
  await ctx.close();
}
fs.writeFileSync('/tmp/uiux-d1/chart-facts8.json',JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1));
await browser.close();
