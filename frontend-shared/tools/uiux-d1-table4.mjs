
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

const read = () => page.evaluate(()=>{
  const t=document.querySelector('.el-table');
  const wrap=t.querySelector('.el-table__body-wrapper .el-scrollbar__wrap');
  const hw=t.querySelector('.el-table__header-wrapper');
  return {body:wrap.scrollLeft, header:hw.scrollLeft,
    th1x: Math.round(t.querySelector('.el-table__header th').getBoundingClientRect().x),
    thLastX: Math.round([...t.querySelectorAll('.el-table__header th')].slice(-1)[0].getBoundingClientRect().x),
    td1x: Math.round(t.querySelector('.el-table__row td').getBoundingClientRect().x),
    tdLastX: Math.round([...t.querySelectorAll('.el-table__row td')].slice(-1)[0].getBoundingClientRect().x),
    scrollLeftMax:(()=>{const b=wrap.scrollLeft;wrap.scrollLeft=99999;const m=wrap.scrollLeft;wrap.scrollLeft=b;return m})()};
});
const box = await page.evaluate(()=>{const t=document.querySelector('.el-table');const b=t.getBoundingClientRect();return {x:b.x,y:b.y+b.height/2,w:b.width};});
console.log('baseline      :', JSON.stringify(await read()));

// A) 滚轮横向（触控板双指/Shift+滚轮）
await page.mouse.move(box.x+box.w/2, box.y);
await page.mouse.wheel(400, 0); await page.waitForTimeout(600);
console.log('after wheelX  :', JSON.stringify(await read()));

// B) 触摸横扫（真实手机手势）
await page.touchscreen.tap(box.x+box.w/2, box.y).catch(()=>{});
await page.evaluate(()=>{const t=document.querySelector('.el-table');const w=t.querySelector('.el-table__body-wrapper .el-scrollbar__wrap');w.scrollLeft=0;t.querySelector('.el-table__header-wrapper').scrollLeft=0;});
await page.waitForTimeout(300);
const cdp = await ctx.newCDPSession(page);
const y = Math.round(box.y), x0 = Math.round(box.x+box.w-30), x1 = Math.round(box.x+30);
await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:x0,y}]});
for(let i=1;i<=8;i++){ await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:Math.round(x0+(x1-x0)*i/8),y}]}); await new Promise(r=>setTimeout(r,40)); }
await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
await page.waitForTimeout(800);
console.log('after touch   :', JSON.stringify(await read()));
await page.screenshot({path:'/tmp/uiux-d1/dashboard-table-390-touch.png'});
await browser.close();
