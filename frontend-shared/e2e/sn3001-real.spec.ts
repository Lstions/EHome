import { test, expect } from '@playwright/test'

const password = process.env.EHOME_E2E_ADMIN_PASSWORD || ''
const username = process.env.EHOME_E2E_ADMIN_USERNAME || 'admin'

test.describe('SN-3001 开发实机控制', () => {
  test.skip(!password, 'requires EHOME_E2E_ADMIN_PASSWORD and the dedicated ehome-dev/C6/SN-3001 rig')

  test('从统一前端创建一次真实读取并显示验证结果', async ({ page }) => {
    const createRequests: string[] = []
    page.on('request', request => {
      if (request.method() === 'POST' && /\/api\/v1\/edge-devices\/1\/operations$/.test(request.url())) {
        createRequests.push(request.url())
      }
    })

    await page.goto('/login')
    await page.locator('input').first().fill(username)
    await page.locator('input[type="password"]').fill(password)
    await page.getByRole('button', { name: /登\s*录/ }).click()
    await page.waitForURL(/\/dashboard/, { timeout: 15_000 })

    await page.goto('/edge-device/1')
    await expect(page.getByText('触发指令', { exact: true })).toHaveCount(0)
    await expect(page.getByText('一次性读取请使用下方受控操作')).toBeVisible()
    const readButton = page.getByRole('button', { name: '读取累计雨量', exact: true })
    await expect(readButton).toBeVisible({ timeout: 20_000 })
    await expect(readButton).toBeEnabled()
    const historyBefore = await page.locator('.el-timeline-item').count()

    const createResponsePromise = page.waitForResponse(response =>
      response.request().method() === 'POST' && /\/api\/v1\/edge-devices\/1\/operations$/.test(response.url()),
    )
    await readButton.click()
    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(202)
    const createEnvelope = await createResponse.json()
    const commandID = createEnvelope?.data?.execution?.command_id as string
    expect(commandID).toMatch(/^[0-9a-f-]{36}$/)
    expect(createRequests).toHaveLength(1)

    const token = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token') || '')
    await expect.poll(async () => {
      return page.evaluate(async ({ id, authToken }) => {
        const response = await fetch(`/api/v1/device-operations/${id}`, { headers: { Authorization: `Bearer ${authToken}` } })
        const body = await response.json()
        return { http: response.status, status: body?.data?.status, result: body?.data?.verified_result }
      }, { id: commandID, authToken: token })
    }, { timeout: 20_000 }).toMatchObject({
      http: 200,
      status: 'SUCCEEDED',
      result: [{ name: 'rainfall', value: 0, unit: 'mm' }],
    })

    await expect.poll(() => page.locator('.el-timeline-item').count(), { timeout: 10_000 }).toBe(historyBefore + 1)
    const newest = page.locator('.el-timeline-item').first()
    await expect(newest).toContainText('read_rainfall')
    await expect(newest).toContainText('SUCCEEDED')
    await expect(newest).toContainText('rainfall=0mm')
    await page.screenshot({ path: '/tmp/e2e-shots/sn3001-real-success.png', fullPage: true })
  })

  test('通过统一前端读取无光照型号的配置基线', async ({ page }) => {
    await page.goto('/login')
    await page.locator('input').first().fill(username)
    await page.locator('input[type="password"]').fill(password)
    await page.getByRole('button', { name: /登\s*录/ }).click()
    await page.waitForURL(/\/dashboard/, { timeout: 15_000 })
    await page.goto('/edge-device/1')

    await expect(page.getByRole('button', { name: '读取光照度', exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: '读取光照偏差', exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: '设置光照偏差', exact: true })).toHaveCount(0)

    const checks = [
      { button: '读取雨量灵敏度', action: 'read_rain_sensitivity', name: 'rain_sensitivity', value: 50, unit: 'raw' },
      { button: '读取设备地址', action: 'read_device_address', name: 'device_address', value: 1, unit: 'address' },
      { button: '读取设备波特率', action: 'read_baud_rate', name: 'baud_rate', value: 4800, unit: 'bit/s' },
    ]

    const token = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token') || '')
    for (const check of checks) {
      const button = page.getByRole('button', { name: check.button, exact: true })
      await expect(button).toBeVisible({ timeout: 20_000 })
      await expect(button).toBeEnabled()
      const createResponsePromise = page.waitForResponse(response =>
        response.request().method() === 'POST' && /\/api\/v1\/edge-devices\/1\/operations$/.test(response.url()),
      )
      await button.click()
      const createResponse = await createResponsePromise
      expect(createResponse.status()).toBe(202)
      const commandID = (await createResponse.json())?.data?.execution?.command_id as string
      await expect.poll(async () => page.evaluate(async ({ id, authToken }) => {
        const response = await fetch(`/api/v1/device-operations/${id}`, { headers: { Authorization: `Bearer ${authToken}` } })
        const body = await response.json()
        return { status: body?.data?.status, result: body?.data?.verified_result }
      }, { id: commandID, authToken: token }), { timeout: 20_000 }).toEqual({
        status: 'SUCCEEDED',
        result: [{ name: check.name, value: check.value, unit: check.unit }],
      })
      const newest = page.locator('.el-timeline-item').first()
      await expect(newest).toContainText(check.action)
      await expect(newest).toContainText('SUCCEEDED')
      await expect(newest).toContainText(`${check.name}=${check.value}${check.unit}`)
    }
    await page.screenshot({ path: '/tmp/e2e-shots/sn3001-real-config-baseline.png', fullPage: true })
  })
})
