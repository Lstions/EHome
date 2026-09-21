import { test, expect, type Page, type Route } from '@playwright/test'

/**
 * task-10 / G4 -- real browser + real HTTP verification of the edit-save loop.
 *
 * Gap: normalize filled an empty device address with the channel bus name
 * (d.hardware_id || d.channel?.hardware_id || ''), so the edit dialog opened
 * with "UART1" and saving wrote the bus name back to the backend.
 *
 * Two evidence layers here:
 *   1) does the real PUT /api/v1/edge-devices/:id body contain hardware_id?
 *   2) does the DB row change after the request hits the real backend?
 *      (checked by the outer bash script with psql before/after)
 *
 * Isolated-stack precondition (set by the outer script): device 1 has
 * hardware_id = '' while its channel hardware_id = 'UART1'.
 */

const BASE = process.env.EHOME_E2E_BASE || 'http://127.0.0.1:18110'
const USER = process.env.EHOME_E2E_USER || 'admin'
const PASS = process.env.EHOME_E2E_PASS || '12345678'
const DEVICE_NAME = process.env.EHOME_E2E_DEVICE || '\u5149\u5b66\u96e8\u91cf\u8ba1'

async function login(page: Page): Promise<void> {
  await page.goto(BASE + '/login')
  await page.waitForSelector('input[type="password"]', { timeout: 20000 })
  await page.locator('input').first().fill(USER)
  await page.locator('input[type="password"]').fill(PASS)
  await page.locator('button:has-text("\u767b\u5f55"), button[type="submit"], button.el-button--primary').first().click()
  await page.waitForURL(/\/(dashboard|node|edge-device)/, { timeout: 25000 })
}

test('G4 edit-save loop: empty device address must not be written back', async ({ page }) => {
  const puts: any[] = []
  await page.route('**/api/v1/edge-devices/*', async (route: Route) => {
    const req = route.request()
    if (req.method() === 'PUT') {
      const entry: any = { url: req.url(), body: JSON.parse(req.postData() || '{}') }
      puts.push(entry)
      // 记录真实后端响应状态 —— 判断"没落库"是因为前端没发，还是因为后端拒绝了
      try {
        const resp = await route.fetch()
        entry.status = resp.status()
        entry.respBody = (await resp.text()).slice(0, 300)
        await route.fulfill({ response: resp })
      } catch (e: any) {
        entry.status = 'fetch-error: ' + (e?.message || e)
        await route.continue()
      }
      return
    }
    await route.continue()
  })

  await login(page)
  await page.goto(BASE + '/edge-device')
  await page.waitForSelector('.device-page', { timeout: 20000 })

  // 必须切到**表格**视图：只有表格行才有 aria-label="编辑 <name>"
  // （卡片视图的编辑按钮是纯文本按钮，没有 aria-label）。
  await page.locator('button[aria-label="\u8868\u683c\u89c6\u56fe"]').first().click()
  await page.waitForTimeout(800)
  const editBtn = page.locator('button[aria-label="\u7f16\u8f91 ' + DEVICE_NAME + '"]').first()
  await editBtn.waitFor({ state: 'visible', timeout: 15000 })
  await editBtn.click()
  await page.waitForTimeout(1200)

  await expect(page.locator('.el-dialog')).toBeVisible()
  const saveBtn = page.locator('.el-dialog button:has-text("\u4fdd\u5b58\u4fee\u6539")').first()
  await saveBtn.waitFor({ state: 'visible', timeout: 10000 })
  await saveBtn.click()
  await page.waitForTimeout(2500)

  console.log('PUT_COUNT=' + puts.length)
  for (const p of puts) console.log('PUT_BODY=' + JSON.stringify(p.body) + ' STATUS=' + p.status + ' RESP=' + p.respBody)
  expect(puts.length, 'must send exactly one real PUT').toBe(1)
  const body = puts[0].body
  console.log('HAS_HARDWARE_ID_KEY=' + Object.prototype.hasOwnProperty.call(body, 'hardware_id'))
  expect(Object.prototype.hasOwnProperty.call(body, 'hardware_id')).toBe(false)
  expect(JSON.stringify(body)).not.toContain('UART1')
})
