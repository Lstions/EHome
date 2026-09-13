import { chromium } from '@playwright/test';
import fs from 'node:fs'; import path from 'node:path';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport:{width:390,height:844}, locale:'zh-CN', hasTouch:true, colorScheme:'light' });
const page = await ctx.newPage(); const errs=[]; const msgs=[];
page.on('pageerror',e=>errs.push(String(e).slice(0,150)));
await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.setAttribute('data-theme','light');document.documentElement.classList.remove('dark');});
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2200);
const out={};

// 1) 配置模板弹窗 overlay 是否可滚动（决定 footer 是否真的不可达）
await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2200);
await page.locator('button:has-text("新建模板")').first().click().catch(()=>{}); await page.waitForTimeout(1500);
out.cfgDialogScroll = await page.evaluate(()=>{
  const ov=document.querySelector('.el-overlay-dialog'), d=document.querySelector('.el-dialog'), b=d&&d.querySelector('.el-dialog__body'), f=d&&d.querySelector('.el-dialog__footer');
  const cs=e=>e?getComputedStyle(e):null;
  const before = ov? Math.round(ov.scrollTop):null;
  if(ov) ov.scrollTop = 99999;
  const after = ov? Math.round(ov.scrollTop):null;
  const fr=f?f.getBoundingClientRect():null;
  return { overlayExists:!!ov, overlayOverflowY:cs(ov)?.overflowY, overlayScrollH:ov?.scrollHeight, overlayClientH:ov?.clientHeight,
    overlayScrollTopBefore:before, overlayScrollTopAfterMax:after, overlayScrollable: !!ov && ov.scrollHeight>ov.clientHeight+2,
    footerTopAfterScroll: fr?Math.round(fr.top):null, footerBottomAfterScroll: fr?Math.round(fr.bottom):null, vh:innerHeight,
    bodyOverflowY:cs(b)?.overflowY, bodyMaxH:cs(b)?.maxHeight, bodyScrollH:b?.scrollHeight, bodyClientH:b?.clientHeight };
});
out.cfgFooterReachable = !!out.cfgDialogScroll.overlayScrollable && out.cfgDialogScroll.footerBottomAfterScroll <= 844;
const f1='/tmp/uiux-d2-probe/mobile-390-light-p7-cfg-scrolled.png'; await page.screenshot({path:f1}); out.shotCfgScrolled=path.basename(f1);
await page.keyboard.press('Escape'); await page.waitForTimeout(700);

// 2) EmptyState CTA「添加节点」点击行为
await page.goto(BASE+'/node',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
await page.fill('input[placeholder="搜索名称/型号"]','zzz-no-match'); await page.waitForTimeout(1500);
const urlBefore = page.url();
const ctaText = await page.evaluate(()=>{const b=document.querySelector('.empty-quick-actions button'); return b?b.textContent.trim():null;});
await page.locator('.empty-quick-actions button').first().click().catch(e=>errs.push('cta:'+e.message));
await page.waitForTimeout(1400);
out.emptyCta = await page.evaluate(()=>({ url:location.href,
  messages:[...document.querySelectorAll('.el-message')].map(m=>(m.textContent||'').trim()),
  emptyTitle:document.querySelector('.empty-title')?.textContent?.trim(),
  emptyDesc:document.querySelector('.empty-description')?.textContent?.trim(),
  kindClass:document.querySelector('.empty-state')?.className }));
out.emptyCta.ctaText=ctaText; out.emptyCta.urlBefore=urlBefore;
const f2='/tmp/uiux-d2-probe/mobile-390-light-p7-empty-cta.png'; await page.screenshot({path:f2}); out.shotEmptyCta=path.basename(f2);

// 3) node-list 表格视图：固定操作列占宽比
await page.locator('button[aria-label="表格视图"]').first().click().catch(()=>{}); await page.waitForTimeout(1300);
out.tableFixedRatio = await page.evaluate(()=>{
  const t=document.querySelector('.el-table'); const fx=t.querySelector('.el-table-fixed-column--right');
  const tr=t.getBoundingClientRect(), fr=fx.getBoundingClientRect();
  const wrap=t.querySelector('.el-scrollbar__wrap');
  // 非固定可见条带
  const strip = Math.round(fr.left - tr.left);
  // 表头里可见（未被固定列遮挡）的列名
  const heads=[...t.querySelectorAll('.el-table__header th')].map(th=>{const r=th.getBoundingClientRect(); return {t:(th.textContent||'').trim(), x:Math.round(r.x), right:Math.round(r.right), covered: r.right>fr.left+1 && r.left<fr.right-1};});
  return { tableW:Math.round(tr.width), fixedW:Math.round(fr.width), stripVisiblePx:strip,
    fixedSharePct: Math.round(fr.width/tr.width*100), scrollW:wrap.scrollWidth, clientW:wrap.clientWidth,
    heads };
});
const f3='/tmp/uiux-d2-probe/mobile-390-light-p7-node-table.png'; await page.screenshot({path:f3}); out.shotTable=path.basename(f3);

// 4) channel-list 启用状态 switch 的可访问名/禁用原因
await page.goto(BASE+'/channel',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2300);
out.channelSwitch = await page.evaluate(()=>{
  const sw=document.querySelector('.el-table__row .el-switch');
  const r=sw.getBoundingClientRect();
  const inner=sw.querySelector('.el-switch__core');
  const ir=inner?inner.getBoundingClientRect():null;
  return { outer:{w:Math.round(r.width),h:Math.round(r.height)}, core: ir?{w:Math.round(ir.width),h:Math.round(ir.height)}:null,
    role:sw.getAttribute('role'), ariaChecked:sw.getAttribute('aria-checked'), ariaLabel:sw.getAttribute('aria-label'),
    ariaDisabled:sw.getAttribute('aria-disabled'), tabindex:sw.getAttribute('tabindex'),
    title:sw.getAttribute('title'), disabledClass:sw.className,
    cellText:sw.closest('td')?.textContent?.trim() };
});
await browser.close();
fs.writeFileSync('/tmp/uiux-d2-probe/p7.json', JSON.stringify({out,errs},null,2));
console.log(JSON.stringify({out,errs},null,1));
