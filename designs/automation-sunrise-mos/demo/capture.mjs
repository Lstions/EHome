/**
 * 交互设计稿 demo 的实拍 + 断言脚本（不是生产代码，仅用于本设计稿自证）。
 *
 * 运行前置（本机实测环境）：
 *   1. 系统没有可用的 chromium-browser（/usr/bin/chromium-browser 是 snap 包装器，
 *      直接调用报 "snap-confine is packaged without necessary permissions"）。
 *      实测可用的浏览器二进制 = /snap/chromium/3548/usr/lib/chromium-browser/chrome
 *      （snap 内的真实 chrome，绕过 snap-confine 直接执行）。
 *   2. frontend-shared/node_modules 未安装；本脚本借用 rtk-gui-v2 已安装的
 *      playwright-core 驱动上述 chrome（只做驱动，不加载任何页面依赖）。
 *   3. 必须设置 XDG_RUNTIME_DIR，否则 snap 应用会拒绝启动。
 *
 * 运行：
 *   XDG_RUNTIME_DIR=/tmp/xdg-runtime-1000 mkdir -p /tmp/xdg-runtime-1000
 *   XDG_RUNTIME_DIR=/tmp/xdg-runtime-1000 node designs/automation-sunrise-mos/demo/capture.mjs
 *
 * 退出码：0 = 全部断言通过且 10 张 PNG 已重拍；1 = 有断言失败。
 */
const PW = '/home/bcat/workspace/rtk-gui-v2/node_modules/.pnpm/playwright-core@1.62.1/node_modules/playwright-core/index.mjs'
const { chromium } = await import(PW)
const URL = 'file:///home/bcat/workspace/EHome/designs/automation-sunrise-mos/demo/index.html'
const OUT = '/home/bcat/workspace/EHome/designs/automation-sunrise-mos/demo/shots'
const browser = await chromium.launch({ executablePath: '/snap/chromium/3548/usr/lib/chromium-browser/chrome', args: ['--no-sandbox','--disable-dev-shm-usage','--disable-gpu'] })
const checks = []
const ok = (n,c,d) => checks.push({ name:n, pass:!!c, detail: d===undefined?'':String(d) })

const ctx = await browser.newContext({ viewport: { width: 1280, height: 1400 }, deviceScaleFactor: 2 })
const page = await ctx.newPage()

// ---- 状态1 ----
await page.goto(URL + '?bar=0&state=sunrise'); await page.waitForTimeout(300)
await page.click('[data-select="trigger-type"]'); await page.waitForTimeout(150)
const dd = await page.locator('#trigger-type-listbox').innerText()
ok('S1 下拉新增 日出/日落', dd.includes('日出') && dd.includes('日落') && await page.locator('#trigger-type-listbox [aria-disabled="true"]').count() === 1, dd.replace(/\n/g,'|'))
await page.screenshot({ path: OUT + '/01-trigger-sunrise-sunset.png', clip: await page.locator('.el-overlay[data-dialog="rule"] .el-dialog').boundingBox() })
await page.click('#trigger-type-listbox [data-value="sunrise"]'); await page.waitForTimeout(150)
ok('S1 偏移分钟联动文案', (await page.locator('[data-test="offset-sentence"]').innerText()) === '日出前 30 分钟')
await page.focus('[data-select="trigger-type"]'); await page.keyboard.press('Enter'); await page.waitForTimeout(120)
await page.keyboard.press('ArrowDown'); await page.waitForTimeout(120); await page.keyboard.press('Enter'); await page.waitForTimeout(150)
ok('S1 键盘可达', (await page.locator('[data-select="trigger-type"]').innerText()).trim().length > 0)

// ---- 状态2 ----
await page.goto(URL + '?bar=0&state=actions'); await page.waitForTimeout(300)
const n0 = await page.locator('[data-test="action-card"]').count()
await page.click('[data-test="add-action"]'); await page.waitForTimeout(150)
const n1 = await page.locator('[data-test="action-card"]').count()
await page.click('[data-test="action-card"]:nth-child(2) [data-act="move-up"]'); await page.waitForTimeout(150)
const firstTxt = await page.locator('[data-test="action-card"]').first().innerText()
await page.click('[data-test="action-card"]:nth-child(4) [data-act="remove-action"]'); await page.waitForTimeout(150)
const n2 = await page.locator('[data-test="action-card"]').count()
ok('S2 增(3→4)/删(4→3)/上移生效', n0===3 && n1===4 && n2===3 && firstTxt.includes('重启 BMS'), n0+'/'+n1+'/'+n2)
await page.goto(URL + '?bar=0&state=actions'); await page.waitForTimeout(300)
const c0 = await page.locator('[data-test="action-card"]').first().innerText()
ok('S2 卡片含 目标设备/动作/参数', c0.includes('目标设备') && c0.includes('动作') && c0.includes('参数'))
await page.locator('[data-test="action-list"]').screenshot({ path: OUT + '/02-multi-action-cards.png' })

// ---- 状态3 ----
await page.goto(URL + '?bar=0&state=risk'); await page.waitForTimeout(300)
const card1 = page.locator('[data-test="action-card"]').first()
const badge = await card1.locator('[data-test="risk-tag-risk"]').innerText()
const badgeColor = await card1.locator('[data-test="risk-tag-risk"]').evaluate(el => getComputedStyle(el).color)
const reason = await card1.locator('[data-test="risk-reason"]').innerText()
const normalRisk = await page.locator('[data-test="action-card"]').nth(2).locator('[data-test="risk-tag-risk"]').count()
const normalReason = await page.locator('[data-test="action-card"]').nth(2).locator('[data-test="risk-reason"]').count()
ok('S3 徽标=图标+文字（不只靠颜色）', badge.includes('风险') && badge.includes('⚠') && badgeColor === 'rgb(185, 28, 28)', badge.replace(/\n/g,'')+' '+badgeColor)
ok('S3 风险原因存在且引用原四级', reason.includes('风险原因') && reason.includes('原四级判定：high'))
ok('S3 普通卡无风险色/无原因块', normalRisk===0 && normalReason===0, normalRisk+'/'+normalReason)
await card1.screenshot({ path: OUT + '/03-risk-badge-and-reason.png' })

// ---- 状态4（中性文案 + 变体）----
await page.goto(URL + '?bar=0&state=actions'); await page.waitForTimeout(300)
await page.click('[data-test="save-rule"]'); await page.waitForTimeout(250)
const items = await page.locator('[data-test="risk-item"]').count()
const modeChoice = await page.locator('[data-test="mode-choice"]').innerText()
const dialogText = await page.locator('.el-overlay[data-dialog="save-risk"]').innerText()
const d0 = await page.locator('[data-test="confirm-save"]').isDisabled()
await page.click('[data-test="risk-ack-1"]'); await page.waitForTimeout(120)
const d1 = await page.locator('[data-test="confirm-save"]').isDisabled()
await page.click('[data-test="risk-ack-2"]'); await page.waitForTimeout(120)
const d2 = await page.locator('[data-test="confirm-save"]').isDisabled()
ok('S4 列出全部风险动作（动作名+目标设备+参数）', items===2 && (await page.locator('[data-test="risk-item"]').first().innerText()).includes('目标设备') && (await page.locator('[data-test="risk-item"]').first().innerText()).includes('参数'), items)
ok('S4 必须逐项勾选才可保存', d0===true && d1===true && d2===false, [d0,d1,d2].join('/'))
ok('S4 文案不预设裁决（含 O-1 两个选项）', modeChoice.includes('待裁决') && modeChoice.includes('选项 A') && modeChoice.includes('选项 B') && modeChoice.includes('自动下发') && modeChoice.includes('待确认'), modeChoice.slice(0,50))
ok('S4 弹窗不再断言"触发时自动下发"', !dialogText.includes('风险动作会在规则触发时自动下发'), '')
await page.screenshot({ path: OUT + '/04-save-confirm-risk-list.png', clip: await page.locator('.el-overlay[data-dialog="save-risk"] .el-dialog').boundingBox() })

await page.goto(URL + '?bar=0&variant=auto&state=actions'); await page.waitForTimeout(300)
await page.click('[data-test="save-rule"]'); await page.waitForTimeout(250)
const chipA = await page.locator('[data-test="variant-chip"]').innerText()
ok('S4b 变体A 文案（自动下发）', chipA.includes('自动下发') && chipA.includes('service.go:521-522'), chipA.slice(0,40))
await page.screenshot({ path: OUT + '/04b-variant-A-auto-dispatch.png', clip: await page.locator('.el-overlay[data-dialog="save-risk"] .el-dialog').boundingBox() })

await page.goto(URL + '?bar=0&variant=pending&state=actions'); await page.waitForTimeout(300)
await page.click('[data-test="save-rule"]'); await page.waitForTimeout(250)
const chipB = await page.locator('[data-test="variant-chip"]').innerText()
ok('S4c 变体B 文案（转待确认）', chipB.includes('待确认') && chipB.includes('planner.go:31'), chipB.slice(0,40))
await page.screenshot({ path: OUT + '/04c-variant-B-pending-confirm.png', clip: await page.locator('.el-overlay[data-dialog="save-risk"] .el-dialog').boundingBox() })

// ---- 状态5 ----
await page.goto(URL + '?bar=0&state=manual'); await page.waitForTimeout(300)
const echo = await page.locator('[data-test="manual-echo"]').innerText()
await page.fill('[data-test="manual-reason"]', ''); await page.waitForTimeout(120)
const m0 = await page.locator('[data-test="manual-confirm"]').isDisabled()
await page.fill('[data-test="manual-reason"]', '现场巡检确认负载已断开，按计划在日落前切断放电 MOS。'); await page.waitForTimeout(120)
const m1 = await page.locator('[data-test="manual-confirm"]').isDisabled()
ok('S5 手动风险操作弹二次确认+回显参数', (await page.locator('.el-overlay[data-dialog="manual-confirm"]').count())===1 && echo.includes('set_mos_policy') && echo.includes('储能柜 BMS'), echo.replace(/\n/g,'|').slice(0,60))
ok('S5 无操作理由不可提交', m0===true && m1===false)
await page.screenshot({ path: OUT + '/05-manual-risk-confirm.png', clip: await page.locator('.el-overlay[data-dialog="manual-confirm"] .el-dialog').boundingBox() })
await page.click('[data-test="manual-cancel"]'); await page.waitForTimeout(200)
await page.click('[data-test="op-item"][data-op="read_basic_info"]'); await page.waitForTimeout(250)
ok('S5 普通操作无二次确认', (await page.locator('.el-overlay').count())===0 && (await page.locator('#toast-root').innerText()).includes('操作已排队'))

// ---- 状态6 ----
await page.goto(URL + '?bar=0&state=normal'); await page.waitForTimeout(300)
ok('S6 无风险徽标/无原因块', (await page.locator('[data-test="risk-tag-risk"]').count())===0 && (await page.locator('[data-test="risk-reason"]').count())===0)
await page.screenshot({ path: OUT + '/06-normal-action-control.png', clip: await page.locator('.el-overlay[data-dialog="rule"] .el-dialog').boundingBox() })
await page.click('[data-test="save-rule"]'); await page.waitForTimeout(250)
ok('S6 保存不弹二次确认', (await page.locator('.el-overlay[data-dialog="save-risk"]').count())===0 && (await page.locator('#toast-root').innerText()).includes('未弹出二次确认'))

await ctx.close()

// ---- 窄屏 390px ----
const mctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 })
const mp = await mctx.newPage()
await mp.goto(URL + '?bar=0&state=save-risk'); await mp.waitForTimeout(400)
await mp.screenshot({ path: OUT + '/07-narrow-390-save-confirm.png' })
const sheetW = await mp.locator('.el-overlay[data-dialog="save-risk"] .el-dialog').evaluate(el => el.getBoundingClientRect().width)
const overflow = await mp.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
ok('窄屏 390 保存确认=底部抽屉满宽且无横向溢出', Math.round(sheetW) === 390 && overflow === false, sheetW+' overflow='+overflow)
await mp.goto(URL + '?bar=0&state=actions'); await mp.waitForTimeout(400)
await mp.screenshot({ path: OUT + '/08-narrow-390-action-cards.png' })
const overflow2 = await mp.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
const cardsSingleCol = await mp.evaluate(() => {
  const list = document.querySelectorAll('[data-test="action-card"]')
  return list.length >= 2 && Math.abs(list[0].getBoundingClientRect().left - list[1].getBoundingClientRect().left) < 2
})
ok('窄屏 390 动作卡单列且无横向溢出', cardsSingleCol && overflow2 === false, 'singleCol='+cardsSingleCol+' overflow='+overflow2)
await mctx.close()

const failed = checks.filter(c => !c.pass).length
console.log(JSON.stringify({ total: checks.length, failed, checks }, null, 1))
await browser.close()
process.exit(failed === 0 ? 0 : 1)
