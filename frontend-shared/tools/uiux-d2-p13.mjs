import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};

// A) device-configs @360：导入按钮是否真的是「裁切」还是可滚动祖先（§2.2 坑）
{
  const ctx = await browser.newContext({ viewport:{width:360,height:800}, locale:'zh-CN', hasTouch:true, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
  out.cfg360 = await page.evaluate(()=>{
    const q=s=>document.querySelector(s); const R=e=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),w:Math.round(r.width),right:Math.round(r.right)};};
    const body=q('.toolbar-card .el-card__body'); const fb=q('.filter-bar'); const fr=q('.filter-right'); const imp=[...document.querySelectorAll('.filter-right button')][0];
    const cardInner=q('.toolbar-card .el-card__body > *');
    // 逐层找可横向滚动的祖先
    const scrollers=[]; let n=imp;
    while(n && n!==document.body){ const cs=getComputedStyle(n);
      if(cs.overflowX!=='visible') scrollers.push({cls:(n.className||'').toString().slice(0,45), ox:cs.overflowX, sw:n.scrollWidth, cw:n.clientWidth, scrollable:n.scrollWidth>n.clientWidth+2}); n=n.parentElement; }
    return { impBtn:R(imp), cardBody:R(body), filterBar:R(fb), filterRight:R(fr), vw:innerWidth,
      docOverflow: document.documentElement.scrollWidth-document.documentElement.clientWidth,
      scrollers,
      filterRightScrollable: fr.scrollWidth>fr.clientWidth+2, filterRightSW:fr.scrollWidth, filterRightCW:fr.clientWidth };
  });
  const f='/tmp/uiux-d2-probe/mobile-360-light-p13-cfg.png'; await page.screenshot({path:f}); out.cfg360shot='mobile-360-light-p13-cfg.png';
  await ctx.close();
}

// B) BMS 详情页容器嵌套（§4.1.5）
{
  const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  await page.goto(BASE+'/edge-device/7053',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(5000);
  out.bms = await page.evaluate(()=>{
    const qa=s=>[...document.querySelectorAll(s)];
    const isCardLike=e=>{const c=getComputedStyle(e); return (c.backgroundColor!=='rgba(0, 0, 0, 0)'&&c.backgroundColor!=='transparent')||(c.borderTopWidth!=='0px'&&c.borderTopStyle!=='none')||c.boxShadow!=='none';};
    const chain=e=>{const o=[];let n=e;while(n&&n!==document.body){ if(isCardLike(n)) o.push({tag:n.tagName.toLowerCase(),cls:(n.className||'').toString().trim().split(/s+/).slice(0,2).join('.'),bg:getComputedStyle(n).backgroundColor,radius:getComputedStyle(n).borderRadius,shadow:getComputedStyle(n).boxShadow.slice(0,30)}); n=n.parentElement;} return o;};
    // 每个 el-card 的“卡片类”祖先层数（不含自身）
    const cards=qa('.el-card').map(c=>({cls:(c.className||'').toString().slice(0,40), self:chain(c).length, ancestors:chain(c.parentElement).length,
      title:(c.querySelector('.el-card__header')?.textContent||'').trim().slice(0,16)}));
    // 最深嵌套（含非 el-card 的灰面板）
    const deepest = qa('.el-card .el-card').length;
    const grayPanels = qa('.el-card [style*="background"], .el-card .fact-row, .el-card .temp-item, .el-card .mos-item').length;
    return { cardCount: qa('.el-card').length, nestedCardCount: deepest, cards, grayPanelLike: grayPanels,
      rowItems: qa('.temp-item, .fact-row, .realtime-item, .cmd-item').slice(0,6).map(e=>({cls:(e.className||'').toString().slice(0,30), depth: chain(e).length, chain: chain(e).slice(0,4)})) };
  });
  await ctx.close();
}
await browser.close();
console.log(JSON.stringify(out,null,1));
