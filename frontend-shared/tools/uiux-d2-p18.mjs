import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'light' });
const page = await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
const out={};
await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
out.statCards = await page.evaluate(()=>[...document.querySelectorAll('.stat-card')].map(c=>({
  text:(c.textContent||'').replace(/\s+/g,' ').trim().slice(0,16),
  cursor:getComputedStyle(c).cursor, cls:c.className, role:c.getAttribute('role'), tabindex:c.getAttribute('tabindex'),
  tabIndexProp:c.tabIndex, hasOnClick: !!c.onclick || c.className.includes('clickable'),
  ariaLabel:c.getAttribute('aria-label')})));
out.selectFocus = await page.evaluate(()=>{
  const w=document.querySelector('.el-select__wrapper');
  const inp=w.querySelector('input');
  return { wrapperTabIndex:w.tabIndex, wrapperRole:w.getAttribute('role'), innerInputTag:inp?inp.tagName:null,
    innerInputTabIndex: inp?inp.tabIndex:null, innerInputDisabled: inp?inp.disabled:null,
    innerInputReadonly: inp?inp.readOnly:null, innerAriaLabel: inp?inp.getAttribute('aria-label'):null,
    innerAriaExpanded: inp?inp.getAttribute('aria-expanded'):null };
});
// 键盘实测：能否 Tab 到 select
await page.evaluate(()=>document.querySelector('.search-input input')?.focus());
const seq=[];
for (let i=0;i<6;i++){ await page.keyboard.press('Tab'); await page.waitForTimeout(120);
  seq.push(await page.evaluate(()=>{const a=document.activeElement; return a.tagName.toLowerCase()+'|'+(a.className||'').toString().slice(0,44)+'|'+(a.getAttribute('aria-label')||a.getAttribute('placeholder')||(a.textContent||'').trim().slice(0,14));})); }
out.tabSeqFromSearch = seq;
// 键盘实测：卡片是否可达
await page.evaluate(()=>document.body.focus());
out.cardReachableByTab = await page.evaluate(()=>{const c=document.querySelector('.collector-card'); c.focus(); return document.activeElement===c;});
// node/1 tab-item 键盘
await page.goto(BASE+'/node/1',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
out.tabItemFocus = await page.evaluate(()=>{const t=document.querySelector('.tab-item'); t.focus(); return {focused:document.activeElement===t, tabIndexProp:t.tabIndex, role:t.getAttribute('role')};});
out.phEdit = await page.evaluate(()=>{const e=document.querySelector('.ph-edit'); if(!e)return null; e.focus(); return {focused:document.activeElement===e, tabIndexProp:e.tabIndex, role:e.getAttribute('role'), aria:e.getAttribute('aria-label'), w:Math.round(e.getBoundingClientRect().width), h:Math.round(e.getBoundingClientRect().height)};});
await browser.close(); console.log(JSON.stringify(out,null,1));
