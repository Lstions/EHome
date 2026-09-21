import { test, expect, type Page, type Route } from '@playwright/test'

/**
 * task-10 / G3 —— **真实浏览器**验证：列表页创建向导的「选择已有通道」分支
 * 提交的 hardware_id 必须不是总线名。
 *
 * 为什么要真实浏览器：这条缺口的判据不是"代码里写了什么"，而是"用户走完这条
 * 创建路径后，HTTP 请求体里到底装了什么"。单测只能证明被 mock 的函数按预期被调用，
 * 证明不了真实 Vue 渲染 + 真实 Element Plus 交互下走上的是哪条分支。
 *
 * 运行方式（受测地址由 EHOME_E2E_BASE 指定，默认指向隔离栈，绝不碰 8080/生产）：
 *   EHOME_E2E_BASE=http://127.0.0.1:18110 \
 *   EHOME_E2E_USER=admin EHOME_E2E_PASS=12345678 \
 *   npx playwright test e2e/g3-existing-channel-address.spec.ts
 *
 * 门禁设计：
 *   1) 用 page.route 截获 POST /api/v1/edge-devices，**记录**真实请求体；
 *      同时用 route.fulfill 直接返回成功，避免真的写库。
 *   2) 断言请求体里的 hardware_id 不是总线名（"UART1"），而是合法设备地址。
 */

const BASE = process.env.EHOME_E2E_BASE || 'http://127.0.0.1:18110'
const USER = process.env.EHOME_E2E_USER || 'admin'
const PASS = process.env.EHOME_E2E_PASS || '12345678'

async function login(page: Page): Promise<void> {
  await page.goto(BASE + '/login')
  await page.waitForSelector('input[type="password"]', { timeout: 20000 })
  await page.locator('input').first().fill(USER)
  await page.locator('input[type="password"]').fill(PASS)
  await page.locator('button:has-text("登录"), button[type="submit"], button.el-button--primary').first().click()
  await page.waitForURL(/\/(dashboard|node|edge-device)/, { timeout: 25000 })
}

test.describe('G3 browser: creation wizard existing-channel branch device address', () => {
  test('submitted hardware_id is not the channel bus name (real wizard walk)', async ({ page }) => {
    const captured: any[] = []
    await page.route('**/api/v1/edge-devices', async (route: Route) => {
      const req = route.request()
      if (req.method() === 'POST') {
        captured.push(JSON.parse(req.postData() || '{}'))
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ code: 200, message: 'success', data: { id: 999001 } }),
        })
        return
      }
      await route.continue()
    })

    await login(page)
    await page.goto(BASE + '/edge-device')
    await page.waitForSelector('.device-page', { timeout: 20000 })

    await page.locator('button:has-text("创建边缘设备")').first().click()
    await page.waitForSelector('.create-device-dialog', { timeout: 10000 })

    await page.locator('.el-dialog button:has-text("下一步")').first().click()
    await page.waitForTimeout(500)

    const parserCards = page.locator('.parser-select-card')
    const parserCount = await parserCards.count()
    expect(parserCount, 'wizard step 1 must offer at least one parser').toBeGreaterThan(0)
    await parserCards.first().click()
    await page.waitForTimeout(400)
    await page.locator('.el-dialog button:has-text("下一步")').first().click()
    await page.waitForTimeout(600)

    const nodeSelect = page.locator('.el-dialog .el-select').first()
    await nodeSelect.click()
    await page.waitForTimeout(400)
    const nodeOption = page.locator('.el-select-dropdown__item:visible').first()
    if (await nodeOption.count()) {
      await nodeOption.click()
      await page.waitForTimeout(1200)
    }

    const existingTab = page.locator('.el-dialog .el-tabs__item:has-text("选择已有通道")')
    await existingTab.click()
    await page.waitForTimeout(600)

    const channelCards = page.locator('.channel-select-card')
    const chCount = await channelCards.count()
    test.skip(chCount === 0, 'no selectable channel on this node; cannot exercise the branch')
    await channelCards.first().click()
    await page.waitForTimeout(400)
    await page.locator('.el-dialog button:has-text("下一步")').first().click()
    await page.waitForTimeout(600)

    // 步骤 3 的可见表单里第一个**可见**文本框就是「边缘设备名称」。
    // 注意：向导各步骤用 v-show 切换，隐藏步骤的 input 仍在 DOM 里，
    // 所以必须用 :visible 限定，否则会填到步骤 0 的隐藏 radio 上。
    const nameInput = page.locator('.el-dialog input[placeholder="请输入边缘设备名称"]:visible')
    await nameInput.waitFor({ state: 'visible', timeout: 15000 })
    await nameInput.fill('G3-browser-verify')
    await page.waitForTimeout(300)
    await page.locator('.el-dialog button:has-text("创建边缘设备")').last().click()
    await page.waitForTimeout(2500)

    expect(captured.length, 'must capture a real POST /edge-devices').toBeGreaterThan(0)
    const body = captured[0]
    // 把真实请求体打出来 —— 对照实验的证据就是这两行 JSON 的差异
    console.log('CAPTURED_POST_BODY=' + JSON.stringify(body))
    expect(String(body.hardware_id), 'hardware_id must be a device address, not a bus name').not.toBe('UART1')
    expect(body.hardware_id).toBe('1')
    expect(body.channel_id).toBeTruthy()
    expect(body.channel).toBeUndefined()
  })
})
