/** 补充取证 #5：亮色移动抽屉（§4.5.1 历史高风险交叉区域） */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE='http://127.0.0.1:8082', OUT=process.env.UIUX_OUT||'/tmp/uiux-d4';
const USER='admin', PASS='UiuxAudit2026!';
const facts={shots:[],measures:{},notes:[]};
const save=()=>fs.writeFileSync(path.join(OUT,'d4-facts5.json'),JSON.stringify(facts,null,2));
const rec=(k,v)=>{facts.measures[k]=v;save();};
const shot=async(p,n)=>{await p.screenshot({path:path.join(OUT,n+'.png')});facts.shots.push(n+'.png');save();};
const browser=await chromium.launch({executablePath:'/snap/bin/chromium',args:['--no-sandbox','--disable-setuid-sandbox']});
for (const theme of ['light','dark']) {
  try {
    const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',colorScheme:theme});
    const page=await ctx.newPage();
    await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
    await page.evaluate(t=>{localStorage.clear();localStorage.setItem('theme',t);},theme);
    await page.reload({waitUntil:'domcontentloaded'});
    await page.waitForSelector('input[placeholder="请输入用户名"]');
    await page.fill('input[placeholder="请输入用户名"]',USER);
    await page.fill('input[placeholder="请输入密码"]',PASS);
    await page.click('button:has-text("登")');
    await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000});
    await page.waitForTimeout(2500);
    await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'});
    await page.waitForSelector('.main-header'); await page.waitForTimeout(1500);
    await page.click('.main-header button[aria-label="打开导航菜单"]');
    await page.waitForTimeout(1000);
    rec('drawer_'+theme, await page.evaluate(()=>{
      const cs=(el)=>{if(!el)return null;const s=getComputedStyle(el);return{bg:s.backgroundColor,color:s.color,fs:s.fontSize};};
      const items=[...document.querySelectorAll('.mobile-sidebar-menu .el-menu-item')];
      return {
        drawer: cs(document.querySelector('.el-drawer')),
        drawerBody: cs(document.querySelector('.el-drawer__body')),
        logoText: cs(document.querySelector('.mobile-logo-text')),
        version: cs(document.querySelector('.mobile-version-info')),
        itemCount: items.length,
        items: items.map(i=>({txt:(i.textContent||'').trim().slice(0,12), ...cs(i), active:i.classList.contains('is-active')})),
        overlay: cs(document.querySelector('.el-overlay')),
        tokenMenuTextColor: getComputedStyle(document.documentElement).getPropertyValue('--el-menu-text-color').trim(),
        tokenSidebarText: getComputedStyle(document.documentElement).getPropertyValue('--sidebar-text').trim(),
        tokenElBg: getComputedStyle(document.documentElement).getPropertyValue('--el-bg-color').trim(),
        tokenTextRegular: getComputedStyle(document.documentElement).getPropertyValue('--el-text-color-regular').trim()
      };
    }));
    await shot(page,'drawer-390-'+theme+'-lightcheck');
    await ctx.close();
  } catch(e){facts.notes.push(theme+': '+String(e).slice(0,300));save();}
}
await browser.close();save();
console.log('done keys: '+Object.keys(facts.measures).join(', '));
console.log('notes: '+JSON.stringify(facts.notes));
