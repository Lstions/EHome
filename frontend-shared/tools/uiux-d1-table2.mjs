
import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1});
const page=await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入用户名"]','admin');
await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
const r = await page.evaluate(()=>{
  const t=document.querySelector('.el-table');
  const bodyTable=t.querySelector('.el-table__body');
  const bw=t.querySelector('.el-table__body-wrapper');
  const firstRow=t.querySelector('.el-table__row');
  const tds=firstRow?[...firstRow.querySelectorAll('td')].map(td=>({txt:td.textContent.trim().slice(0,14),w:Math.round(td.getBoundingClientRect().width),x:Math.round(td.getBoundingClientRect().x),right:Math.round(td.getBoundingClientRect().right)})):[];
  // 主体是否能横向滚动到底
  const before=bw.scrollLeft; bw.scrollLeft=9999; const after=bw.scrollLeft; bw.scrollLeft=before;
  const hw=t.querySelector('.el-table__header-wrapper');
  const hBefore=hw.scrollLeft; hw.scrollLeft=9999; const hAfter=hw.scrollLeft; hw.scrollLeft=hBefore;
  return {
    bodyTableW: bodyTable?Math.round(bodyTable.getBoundingClientRect().width):null,
    rowW: firstRow?Math.round(firstRow.getBoundingClientRect().width):null,
    tds,
    bodyScrollLeftMax: after, headerScrollLeftMax: hAfter,
    bodyScrollable: bw.scrollWidth>bw.clientWidth,
    headerScrollable: hw.scrollWidth>hw.clientWidth,
    scrollbarVisibleBody: bw.offsetHeight-bw.clientHeight,
    scrollbarVisibleHeader: hw.offsetHeight-hw.clientHeight,
    lastTdVisible: tds.length? tds[tds.length-1].right <= 341 : null,
  };
});
console.log(JSON.stringify(r,null,1));
await page.screenshot({path:'/tmp/uiux-d1/dashboard-table-390.png', clip:{x:0,y:600,width:390,height:244}}).catch(()=>{});
await browser.close();
