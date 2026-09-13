import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const b=await chromium.launch({executablePath:'/snap/bin/chromium',args:['--no-sandbox','--disable-setuid-sandbox']});
const ctx=await b.newContext({viewport:{width:1440,height:900},locale:'zh-CN'});
const page=await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>localStorage.clear());
await page.reload({waitUntil:'domcontentloaded'});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin');
await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000});
await page.waitForTimeout(2500);
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'});
await page.waitForSelector('.sidebar'); await page.waitForTimeout(1500);
console.log('menu UL:', JSON.stringify(await page.evaluate(()=>{
  const ul=document.querySelector('.sidebar-menu');
  return {tabindex: ul.getAttribute('tabindex'), tabIndexProp: ul.tabIndex, focusable: ul.tabIndex>=0};
})));
// 从 body 连按 30 次 Tab，记录是否出现过 sidebar 菜单项（用 URL 变化 + 元素归属判断）
await page.evaluate(()=>{ if(document.activeElement) document.activeElement.blur(); });
const hits=[];
for(let i=0;i<30;i++){
  await page.keyboard.press('Tab');
  const info=await page.evaluate(()=>{
    const a=document.activeElement;
    if(!a) return null;
    const inSidebar = !!a.closest('.sidebar');
    return {i:null, tag:a.tagName, cls:String(a.className).slice(0,44), inSidebar, txt:(a.textContent||'').replace(/\s+/g,' ').trim().slice(0,14)};
  });
  info.i=i+1; hits.push(info);
  if(info.inSidebar) break;
}
console.log('Tab 序列（前若干）:');
hits.slice(0,22).forEach(h=>console.log('  ',h.i, h.tag, h.cls, '| inSidebar=',h.inSidebar, '|', h.txt));
console.log('30 次 Tab 内是否到达侧栏菜单项:', hits.some(h=>h.inSidebar));
// 点击菜单项确认 router 导航可用（对照）
await page.click('.sidebar .el-menu-item:nth-child(2)');
await page.waitForTimeout(1500);
console.log('鼠标点击菜单项后 url:', page.url());
await b.close();
