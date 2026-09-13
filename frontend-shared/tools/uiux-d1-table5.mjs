
import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1,hasTouch:true,isMobile:true});
const page=await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);

// 把表格滚进视口
await page.evaluate(()=>{const t=document.querySelector('.el-table'); t.scrollIntoView({block:'center'});});
await page.waitForTimeout(800);
const rd = () => page.evaluate(()=>{
  const t=document.querySelector('.el-table');
  const wrap=t.querySelector('.el-table__body-wrapper .el-scrollbar__wrap');
  const hw=t.querySelector('.el-table__header-wrapper');
  const ths=[...t.querySelectorAll('.el-table__header th')];
  const tds=[...t.querySelectorAll('.el-table__row td')];
  return {body:wrap.scrollLeft, header:hw.scrollLeft,
    th1x: Math.round(ths[0].getBoundingClientRect().x), thLastx: Math.round(ths[ths.length-1].getBoundingClientRect().x),
    td1x: Math.round(tds[0].getBoundingClientRect().x), tdLastx: Math.round(tds[tds.length-1].getBoundingClientRect().x),
    thVisible: ths.filter(e=>{const r=e.getBoundingClientRect();return r.x>=41&&r.right<=341}).map(e=>e.textContent.trim()),
    tdVisible: tds.filter(e=>{const r=e.getBoundingClientRect();return r.x>=41&&r.right<=341}).map(e=>e.textContent.trim().slice(0,12)),
    scrollLeftMax:(()=>{const b=wrap.scrollLeft;wrap.scrollLeft=99999;const m=wrap.scrollLeft;wrap.scrollLeft=b;return m})(),
    columnResize: t.className.includes('column')};});
const bb = await page.evaluate(()=>{const t=document.querySelector('.el-table');const bw=t.querySelector('.el-table__body-wrapper');const b=bw.getBoundingClientRect();return {x:Math.round(b.x),y:Math.round(b.y),w:Math.round(b.width),h:Math.round(b.height)};});
console.log('bodyWrapper rect:', JSON.stringify(bb));
console.log('baseline    :', JSON.stringify(await rd()));
const cy = bb.y + Math.min(bb.h/2, 40);
// A) 横向滚轮
await page.mouse.move(bb.x + bb.w/2, cy);
await page.mouse.wheel(300, 0); await page.waitForTimeout(700);
console.log('wheelX      :', JSON.stringify(await rd()));
// B) 触摸横扫
await page.evaluate(()=>{const t=document.querySelector('.el-table');t.querySelector('.el-table__body-wrapper .el-scrollbar__wrap').scrollLeft=0;t.querySelector('.el-table__header-wrapper').scrollLeft=0;});
const cdp = await ctx.newCDPSession(page);
const x0 = bb.x+bb.w-25, x1 = bb.x+25;
await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:x0,y:cy}]});
for(let i=1;i<=10;i++){ await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:Math.round(x0+(x1-x0)*i/10),y:cy}]}); await new Promise(r=>setTimeout(r,30)); }
await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
await page.waitForTimeout(900);
console.log('touchSwipe  :', JSON.stringify(await rd()));
await page.screenshot({path:'/tmp/uiux-d1/dashboard-table-390-inscroll.png'});
await browser.close();
