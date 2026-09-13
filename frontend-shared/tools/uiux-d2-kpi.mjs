import { chromium } from '@playwright/test';
const BASE='http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const out={};
for (const [vp,W,H] of [['desktop-1440',1440,900],['mobile-390',390,844],['mobile-360',360,800],['tablet-768',768,1024]]) {
  const ctx = await browser.newContext({ viewport:{width:W,height:H}, locale:'zh-CN', hasTouch:W<=480, colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  const rec={};
  for (const [name,path] of [['node','/node'],['edge','/edge-device']]) {
    await page.goto(BASE+path,{waitUntil:'domcontentloaded'}); await page.waitForTimeout(2600);
    rec[name]=await page.evaluate(()=>{
      const g=document.querySelector('.stats-row'); const cs=getComputedStyle(g);
      const kids=[...g.children].map(c=>{const r=c.getBoundingClientRect(); return {w:Math.round(r.width),h:Math.round(r.height),y:Math.round(r.y)};});
      const ys=[...new Set(kids.map(k=>k.y))];
      const icon=document.querySelector('.stat-icon'), val=document.querySelector('.stat-value'), lab=document.querySelector('.stat-label');
      return { cols: cs.gridTemplateColumns, gap: cs.gap, count: kids.length, kids, distinctRows: ys.length,
        iconW: icon?Math.round(icon.getBoundingClientRect().width):null, valFs: val?getComputedStyle(val).fontSize:null,
        labFs: lab?getComputedStyle(lab).fontSize:null, containerW: Math.round(g.getBoundingClientRect().width) };
    });
  }
  out[vp]=rec; await ctx.close();
}
await browser.close(); console.log(JSON.stringify(out,null,1));
