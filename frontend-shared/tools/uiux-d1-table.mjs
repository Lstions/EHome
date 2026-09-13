
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', CHROME='/snap/bin/chromium';
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const ctx=await browser.newContext({viewport:{width:390,height:844},locale:'zh-CN',deviceScaleFactor:1});
const page=await ctx.newPage();
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);},'light');
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
await page.goto(BASE+'/dashboard',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3000);
const r = await page.evaluate(()=>{
  const t=document.querySelector('.el-table'); if(!t) return {err:'no table'};
  const bw=t.querySelector('.el-table__body-wrapper'), hw=t.querySelector('.el-table__header-wrapper');
  const inner=t.querySelector('.el-table__inner-wrapper')||t.querySelector('.el-table__body');
  const cols=[...t.querySelectorAll('.el-table__header col')].map(c=>c.getAttribute('width')||getComputedStyle(c).width);
  const heads=[...t.querySelectorAll('.el-table__header th')].map(th=>({txt:th.textContent.trim().slice(0,8),w:Math.round(th.getBoundingClientRect().width),right:Math.round(th.getBoundingClientRect().right)}));
  const rows=[...t.querySelectorAll('.el-table__row')].length;
  const cards=[...t.closest('.el-card').parentElement.children];
  return {
    tableRect:(()=>{const b=t.getBoundingClientRect();return {w:Math.round(b.width),right:Math.round(b.right)}})(),
    headerWrapper: hw?{sw:hw.scrollWidth,cw:hw.clientWidth,ox:getComputedStyle(hw).overflowX}:null,
    bodyWrapper: bw?{sw:bw.scrollWidth,cw:bw.clientWidth,ox:getComputedStyle(bw).overflowX}:null,
    inner: inner?{sw:inner.scrollWidth,cw:inner.clientWidth,w:Math.round(inner.getBoundingClientRect().width)}:null,
    headerTable: (()=>{const ht=t.querySelector('.el-table__header');return ht?{w:Math.round(ht.getBoundingClientRect().width),right:Math.round(ht.getBoundingClientRect().right)}:null})(),
    cols, heads, rows,
    parentCls: t.parentElement.className, parentOfParent: t.parentElement.parentElement.className,
    docOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    viewport: window.innerWidth,
    hasMobileWrapper: !!document.querySelector('.mobile-table-wrapper'),
    hasMobileHint: !!document.querySelector('.mobile-table-hint'),
    trendEmpty: !!document.querySelector('.empty-state'),
    tableEmpty: !!document.querySelector('.el-table__empty-block'),
    tableEmptyText: (document.querySelector('.el-table__empty-text')||{}).textContent,
  };
});
console.log(JSON.stringify(r,null,1));
// 也测 360
await page.setViewportSize({width:360,height:800}); await page.waitForTimeout(900);
const r2 = await page.evaluate(()=>{const t=document.querySelector('.el-table');const bw=t&&t.querySelector('.el-table__body-wrapper');const hw=t&&t.querySelector('.el-table__header-wrapper');
 return {vp:window.innerWidth, headerWrapper:hw?{sw:hw.scrollWidth,cw:hw.clientWidth}:null, bodyWrapper:bw?{sw:bw.scrollWidth,cw:bw.clientWidth}:null, headerTableW:(()=>{const ht=t&&t.querySelector('.el-table__header');return ht?Math.round(ht.getBoundingClientRect().width):null})(), docOverflow:document.documentElement.scrollWidth-document.documentElement.clientWidth};});
console.log('360:', JSON.stringify(r2));
await browser.close();
