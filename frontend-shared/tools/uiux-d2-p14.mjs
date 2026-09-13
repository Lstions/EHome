import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};
for (const [vp,W,H] of [['mobile-360',360,800],['mobile-390',390,844]]) {
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:true, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  await page.goto(BASE+'/device-configs',{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2400);
  out[vp]=await page.evaluate(()=>{
    const q=s=>document.querySelector(s); const R=e=>{const r=e.getBoundingClientRect();return{x:Math.round(r.x),w:Math.round(r.width),right:Math.round(r.right),h:Math.round(r.height)};};
    const fr=q('.filter-right'), body=q('.toolbar-card .el-card__body'), bar=q('.filter-bar');
    const btns=[...fr.querySelectorAll('button')].map(b=>({t:b.textContent.trim(), ...R(b)}));
    return { vw:innerWidth, filterRight:R(fr), frSW:fr.scrollWidth, frCW:fr.clientWidth, body:R(body), bodySW:body.scrollWidth, bodyCW:body.clientWidth,
      bar:R(bar), btns,
      btnLeftOfBody: btns.filter(b=>b.x < R(body).x-1),
      minBtnX: Math.min(...btns.map(b=>b.x)), bodyLeft: R(body).x,
      docOverflow: document.documentElement.scrollWidth-document.documentElement.clientWidth,
      cardOverflowX: getComputedStyle(q('.toolbar-card')).overflowX };
  });
  await ctx.close();
}
await browser.close(); console.log(JSON.stringify(out,null,1));
