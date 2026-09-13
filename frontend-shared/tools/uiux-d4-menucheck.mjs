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
console.log(JSON.stringify(await page.evaluate(()=>{
  const ul=document.querySelector('.sidebar-menu');
  const items=[...document.querySelectorAll('.sidebar .el-menu-item')];
  return {
    menuTag: ul?ul.tagName:null, menuRole: ul?ul.getAttribute('role'):null, menuCls: ul?ul.className:null,
    tabindexes: items.map(i=>i.getAttribute('tabindex')),
    itemRoles: [...new Set(items.map(i=>i.getAttribute('role')))],
    ariaOrientation: ul?ul.getAttribute('aria-orientation'):null,
    firstItemAria: items[0]?{role:items[0].getAttribute('role'),ti:items[0].getAttribute('tabindex')}:null
  };
}),null,1));
// 键盘：Tab 到菜单后按 ArrowDown 是否移动焦点
await page.evaluate(()=>{const i=document.querySelector('.sidebar .el-menu-item'); i.focus();});
await page.waitForTimeout(300);
const before=await page.evaluate(()=>document.activeElement.textContent.trim());
await page.keyboard.press('ArrowDown');
await page.waitForTimeout(400);
const after=await page.evaluate(()=>({txt:document.activeElement.textContent.trim(),ti:document.activeElement.getAttribute('tabindex')}));
console.log('ArrowDown focus:', before, '->', JSON.stringify(after));
// 通知项是否可被 Tab 到达
await page.click('.main-header button[aria-label="打开通知中心"]');
await page.waitForTimeout(800);
const reached=await page.evaluate(async()=>{
  const items=[...document.querySelectorAll('.notification-item')];
  return {count:items.length, anyFocusable:items.some(i=>i.tabIndex>=0), roles:[...new Set(items.map(i=>i.getAttribute('role')))]};
});
console.log('notification items:', JSON.stringify(reached));
await b.close();
