import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:390,height:844}, locale:'zh-CN', hasTouch:true, colorScheme:'light' });
const page = await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2200);
const out={};

// A) 配置模板弹窗：真实滚动可达性（wheel + window.scrollTo + overlay scroll）
await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2200);
await page.locator('button:has-text("新建模板")').first().click().catch(()=>{}); await page.waitForTimeout(1600);
out.beforeWheel = await page.evaluate(()=>{const f=document.querySelector('.el-dialog__footer'); const r=f.getBoundingClientRect(); return {footerTop:Math.round(r.top),footerBottom:Math.round(r.bottom),vh:innerHeight,winY:Math.round(scrollY)};});
await page.mouse.move(195,700); await page.mouse.wheel(0,600); await page.waitForTimeout(700);
out.afterWheel = await page.evaluate(()=>{const f=document.querySelector('.el-dialog__footer'); const r=f.getBoundingClientRect();
  const ovl=[...document.querySelectorAll('.el-overlay,.el-overlay-dialog')].map(o=>({cls:o.className.slice(0,40),oy:getComputedStyle(o).overflowY,sh:o.scrollHeight,ch:o.clientHeight,st:Math.round(o.scrollTop)}));
  return {footerTop:Math.round(r.top),footerBottom:Math.round(r.bottom),vh:innerHeight,winY:Math.round(scrollY),overlays:ovl};});
await page.evaluate(()=>window.scrollTo(0,99999)); await page.waitForTimeout(400);
out.afterWindowScroll = await page.evaluate(()=>{const f=document.querySelector('.el-dialog__footer'); const r=f.getBoundingClientRect(); return {footerBottom:Math.round(r.bottom),vh:innerHeight,winY:Math.round(scrollY),docScrollH:document.documentElement.scrollHeight};});
const f=await page.screenshot({path:'/tmp/uiux-d2-probe/mobile-390-light-p8-cfg-dialog.png'}); out.shot='mobile-390-light-p8-cfg-dialog.png';

// 键盘可达：Tab 到 footer 按钮
await page.keyboard.press('Escape'); await page.waitForTimeout(600);

// B) channel switch outerHTML
await page.goto(BASE+'/channel',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
out.switchHTML = await page.evaluate(()=>{const sw=document.querySelector('.el-table__row .el-switch'); return sw?sw.outerHTML.slice(0,500):null;});
out.switchRoleInherit = await page.evaluate(()=>{const sw=document.querySelector('.el-table__row .el-switch'); return {role:sw.getAttribute('role'), ariaChecked:sw.getAttribute('aria-checked'), ariaDisabled:sw.getAttribute('aria-disabled'), tabindex:sw.tabIndex};});

// C) node-list 卡片「配置」按钮：是否跳转到不存在的能力
await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2300);
await page.locator('.collector-card .el-button:has-text("配置")').first().click().catch(()=>{}); await page.waitForTimeout(1800);
out.configClick = await page.evaluate(()=>({url:location.href, activeTab:document.querySelector('.tab-item.active')?.textContent?.trim()}));
await page.goBack().catch(()=>{}); await page.waitForTimeout(1200);
await page.locator('.collector-card .el-button:has-text("升级")').first().click().catch(()=>{}); await page.waitForTimeout(1800);
out.otaClick = await page.evaluate(()=>({url:location.href, h2:document.querySelector('h2')?.textContent?.trim(), h1:document.querySelector('h1')?.textContent?.trim()}));
await browser.close();
fs.writeFileSync('/tmp/uiux-d2-probe/p8.json',JSON.stringify(out,null,2));
console.log(JSON.stringify(out,null,1));
