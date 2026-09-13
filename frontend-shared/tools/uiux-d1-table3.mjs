
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

const probe = async (label, rootSel) => {
  return await page.evaluate((sel)=>{
    const t=document.querySelector(sel);
    const r={};
    r.table = t? {w:Math.round(t.getBoundingClientRect().width)} : null;
    const list=[['.el-table__body-wrapper','bodyWrapper'],['.el-table__header-wrapper','headerWrapper'],
      ['.el-table__body-wrapper .el-scrollbar__wrap','bodyScrollbarWrap'],
      ['.el-table__body-wrapper .el-scrollbar','bodyScrollbar'],
      ['.el-table__body','bodyTable'],
      ['.el-table__header','headerTable']];
    for(const [s,name] of list){
      const e=t&&t.querySelector(s);
      if(!e){ r[name]=null; continue; }
      const cs=getComputedStyle(e);
      const before=e.scrollLeft; e.scrollLeft=99999; const max=e.scrollLeft; e.scrollLeft=before;
      r[name]={sw:e.scrollWidth,cw:e.clientWidth,ox:cs.overflowX,oy:cs.overflowY,pos:cs.position,scrollLeftMax:max,
        w:Math.round(e.getBoundingClientRect().width),x:Math.round(e.getBoundingClientRect().x)};
    }
    return r;
  }, rootSel);
};
console.log('=== dashboard ===');
console.log(JSON.stringify(await probe('dashboard','.el-table'),null,1));

// 真实手势：在表体上横向拖拽（touch）后读取表体滚动位置
const box = await page.evaluate(()=>{const t=document.querySelector('.el-table');const b=t.getBoundingClientRect();return {x:b.x+b.width/2,y:b.y+b.height/2};});
await page.mouse.move(box.x+120, box.y); await page.mouse.down();
await page.mouse.move(box.x-120, box.y, {steps:12}); await page.mouse.up();
await page.waitForTimeout(500);
console.log('after mouse drag:', JSON.stringify(await page.evaluate(()=>{
  const t=document.querySelector('.el-table');
  const bw=t.querySelector('.el-table__body-wrapper .el-scrollbar__wrap')||t.querySelector('.el-table__body-wrapper');
  const hw=t.querySelector('.el-table__header-wrapper');
  return {bodyScrollLeft:bw.scrollLeft, headerScrollLeft:hw.scrollLeft,
    firstTdX: Math.round(t.querySelector('.el-table__row td').getBoundingClientRect().x),
    headerThX: Math.round(t.querySelector('.el-table__header th').getBoundingClientRect().x)};
})));

console.log('=== /data ===');
await page.goto(BASE+'/data',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(1500);
await page.click('.el-form-item:has-text("设备") .el-select'); await page.waitForTimeout(700);
await page.click('.el-select-dropdown__item'); await page.waitForTimeout(300); await page.keyboard.press('Escape');
await page.click('button:has-text("查询")'); await page.waitForTimeout(3000);
console.log(JSON.stringify(await probe('data','.el-table'),null,1));
console.log('mobile wrapper present:', await page.evaluate(()=>({w:document.querySelectorAll('.mobile-table-wrapper').length,h:document.querySelectorAll('.mobile-table-hint').length,hint:(document.querySelector('.mobile-table-hint')||{}).textContent})));
await browser.close();
