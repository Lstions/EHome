
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', CHROME='/snap/bin/chromium';
const lum = (rgb) => { const m=rgb.match(/[\d.]+/g).map(Number); const s=m.slice(0,3).map(v=>{v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4)}); return 0.2126*s[0]+0.7152*s[1]+0.0722*s[2]; };
const ratio=(a,b)=>{const l1=lum(a),l2=lum(b);return +(((Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05))).toFixed(2)};
const browser=await chromium.launch({executablePath:CHROME,args:['--no-sandbox','--disable-setuid-sandbox','--font-render-hinting=none']});
const out={};
for (const theme of ['light','dark']) {
  const ctx=await browser.newContext({viewport:{width:1440,height:900},locale:'zh-CN',deviceScaleFactor:1,colorScheme:theme});
  const page=await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1500);
  const t={};
  for (const [name,url] of [['dashboard','/dashboard'],['monitor','/monitor'],['data','/data']]) {
    await page.goto(BASE+url,{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2500);
    t[name] = await page.evaluate(()=>{
      const effBg = (el) => { let e=el; while(e && e!==document.documentElement){ const c=getComputedStyle(e).backgroundColor; const m=c.match(/[\d.]+/g); if(m && (m.length<4 || Number(m[3])>0.5) && !/rgba\(0, 0, 0, 0\)/.test(c)) return c; e=e.parentElement; } return getComputedStyle(document.body).backgroundColor; };
      const items=[];
      // 浅底徽标/标签/文本
      document.querySelectorAll('.el-tag, .el-alert, .alert-value, .alert-label, .control-metric strong, .control-metric span, .stat-value, .stat-label, .mobile-table-hint, .error-text, .status-label, .el-descriptions__label, .el-descriptions__content, .el-progress__text, .footer-info span, .trend-card-header span, .raw-data, .device-link').forEach(e=>{
        const r=e.getBoundingClientRect(); if(r.width<=0||r.height<=0) return;
        const cs=getComputedStyle(e);
        items.push({cls:(e.className||'').toString().split(' ').slice(0,2).join('.')||e.tagName.toLowerCase(),
          text:e.textContent.trim().replace(/\s+/g,' ').slice(0,22),
          fg:cs.color, bg:effBg(e), font:cs.fontSize, weight:cs.fontWeight});
      });
      return items;
    });
  }
  out[theme]=t; await ctx.close();
}
await browser.close();
// 计算对比度
const report={};
for (const theme of ['light','dark']) { report[theme]={};
  for (const page of ['dashboard','monitor','data']) {
    report[theme][page]=(out[theme][page]||[]).map(i=>{
      let cr=null; try{ cr=ratio(i.fg,i.bg); }catch(e){}
      return {...i, contrast:cr, aa_normal: cr!==null? cr>=4.5 : null, aa_large: cr!==null? cr>=3 : null};
    }).filter(i=>i.contrast!==null && i.contrast<4.5);
  }
}
fs.writeFileSync('/tmp/uiux-d1/contrast.json',JSON.stringify({raw:out,low:report},null,2));
for (const theme of ['light','dark']) for (const page of ['dashboard','monitor','data']) {
  const low=report[theme][page];
  console.log('### '+theme+' '+page+' 低于 4.5:1 的条目 ('+low.length+')');
  const seen=new Set();
  for (const i of low){ const k=i.cls+i.fg+i.bg; if(seen.has(k))continue; seen.add(k);
    console.log('   '+String(i.contrast).padStart(5)+':1  fg='+i.fg+' bg='+i.bg+'  font='+i.font+' w='+i.weight+'  ['+i.cls+'] "'+i.text+'"'); }
}
await browser.close();
