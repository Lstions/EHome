
import { chromium } from '@playwright/test'
const EXE = '/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'
const dir = '/mnt/storage/WorkSpace/EHome/.analysis/chart-mobile/prototype'
const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox','--disable-dev-shm-usage','--allow-file-access-from-files'] })
for (const [w, h] of [[393, 851], [420, 910]]) {
  const ctx = await browser.newContext({ viewport:{ width:w, height:h }, deviceScaleFactor:3, isMobile:true, hasTouch:true, colorScheme:'dark' })
  const page = await ctx.newPage()
  page.on('pageerror', e => console.log('[pageerror]', String(e).slice(0,200)))
  await page.goto('file://' + dir + '/index.html')
  await page.waitForFunction(() => window.__ready === true, null, { timeout: 20000 })
  await page.waitForTimeout(900)
  await page.screenshot({ path: dir + '/proto-' + w + '-page.png' })
  for (const id of ['card-temp', 'card-soc', 'card-p0']) await page.locator('#' + id).screenshot({ path: dir + '/proto-' + w + '-' + id + '.png' })
  const m = await page.evaluate(() => {
    const out = []
    for (const id of ['plot-temp','plot-soc','plot-p0']) {
      const el = document.getElementById(id)
      const inst = echarts.getInstanceByDom(el)
      const r = el.getBoundingClientRect()
      const o = inst.getOption()
      const gridArr = Array.isArray(o.grid) ? o.grid : [o.grid]
      const grids = gridArr.map((g,i) => { const rect = inst.getModel().getComponent('grid', i).coordinateSystem.getRect(); return { x:+rect.x.toFixed(1), y:+rect.y.toFixed(1), w:+rect.width.toFixed(1), h:+rect.height.toFixed(1) } })
      const plotArea = grids.reduce((s,g)=>s+g.w*g.h,0)
      let ticks = null
      try { ticks = inst.getModel().getComponent('xAxis',0).axis.scale.getTicks().length } catch {}
      out.push({ id, container:{ w:+r.width.toFixed(1), h:+r.height.toFixed(1) }, grids, plotRatio:+(plotArea/(r.width*r.height)*100).toFixed(1), xTicks:ticks })
    }
    return out
  })
  console.log(w, JSON.stringify(m))
  await ctx.close()
}
await browser.close()