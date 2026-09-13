/**
 * 广播2 自查：接口失败是否被伪装成"一切正常"。
 * 逐页拦截主 API 返回 500，取 DOM 事实：KPI 值 / 摘要文案 / 错误态 / 重试入口 / 空态类型。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082', OUT='/tmp/uiux-d2-probe';
const CASES = [
  { name:'node-list',        route:'/node',           api:'**/api/v1/nodes*' },
  { name:'edge-device-list', route:'/edge-device',    api:'**/api/v1/edge-devices*' },
  { name:'logical-device-list', route:'/logical-device', api:'**/api/v1/logical-devices*' },
  { name:'channel-list',     route:'/channel',        api:'**/api/v1/channels*' },
  { name:'device-configs',   route:'/device-configs', api:'**/api/v1/device-configs*' },
  { name:'node-detail',      route:'/node/1',         api:'**/api/v1/nodes/*' },
  { name:'edge-detail',      route:'/edge-device/7053', api:'**/api/v1/edge-devices/*' },
];

const FACTS = () => {
  const q=s=>document.querySelector(s), qa=s=>[...document.querySelectorAll(s)];
  const T=e=>e?(e.textContent||'').trim().replace(/\s+/g,' '):null;
  const vis=e=>{if(!e)return false;const r=e.getBoundingClientRect();return r.width>0&&r.height>0;};
  const bodyT=(document.body.innerText||'').replace(/\s+/g,' ');
  // 自研空态 vs el-empty（广播3）
  const selfEmpty=qa('.empty-state').filter(vis);
  const elEmpty=qa('.el-empty').filter(vis);
  // "正常/无异常"类摘要
  const okWords=['运行正常','全部正常','均在线','暂无采集错误','无异常','正常','系统正常'];
  const okHits=okWords.filter(w=>bodyT.includes(w));
  // 绿色语义色（成功）出现在摘要卡
  const greenEls=qa('.el-card, .stat-card, .summary, [class*="summary"], [class*="alert"]')
    .filter(vis).map(e=>({t:T(e).slice(0,40), color:getComputedStyle(e).color, bg:getComputedStyle(e).backgroundColor}))
    .filter(x=>/rgb\(103, 194, 58\)|rgb\(103,194,58\)/.test(x.color));
  return {
    kpiValues: qa('.stat-value, .stat-card .stat-value').filter(vis).map(e=>T(e)),
    kpiCount: qa('.stat-value').filter(vis).length,
    selfEmptyCount: selfEmpty.length, selfEmptyText: selfEmpty.slice(0,2).map(e=>T(e).slice(0,80)),
    elEmptyCount: elEmpty.length, elEmptyText: elEmpty.slice(0,2).map(e=>T(e).slice(0,80)),
    emptyKinds: selfEmpty.map(e=>e.className).slice(0,3),
    errorStateCount: qa('.el-result, [class*="error-state"], [class*="error-page"], .el-alert--error').filter(vis).length,
    retryCount: qa('button').filter(vis).filter(b=>/重试|重新加载|重载/.test(T(b)||'')).length,
    retryLabels: qa('button').filter(vis).map(T).filter(t=>/重试|重新加载|重载/.test(t||'')),
    okWordHits: okHits,
    greenSummaryEls: greenEls,
    rows: qa('.el-table__row').length,
    cards: qa('.collector-card, .device-card, .config-card').length,
    bodyHead: bodyT.slice(0, 260),
  };
};

const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const report={};
for (const theme of ['light']) {
  const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(1800);
  for (const c of CASES) {
    await page.route(c.api, r => r.fulfill({ status:500, contentType:'application/json', body: JSON.stringify({code:500,message:'injected failure'}) }));
    await page.goto(BASE+c.route,{waitUntil:'domcontentloaded'}); await page.waitForTimeout(3200);
    report[c.name] = { route:c.route, api:c.api, ...(await page.evaluate(FACTS)) };
    const f=OUT+'/fail500-'+c.name+'.png'; await page.screenshot({path:f}); report[c.name].shot='fail500-'+c.name+'.png';
    await page.unroute(c.api);
  }
  await ctx.close();
}
await browser.close();
fs.writeFileSync(OUT+'/p17-failure-injection.json', JSON.stringify(report,null,2));
console.log('done', Object.keys(report).length);
