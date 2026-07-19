import { test, expect, type Page } from '@playwright/test'

const password = process.env.EHOME_E2E_ADMIN_PASSWORD || ''
const username = process.env.EHOME_E2E_ADMIN_USERNAME || 'admin'

test.describe('SN-3001 开发实机写入与读回', () => {
  test.skip(!password, 'requires EHOME_E2E_ADMIN_PASSWORD and the dedicated ehome-dev/C6/SN-3001 rig')

  async function login(page: Page) {
    await page.goto('/login')
    await page.locator('input').first().fill(username)
    await page.locator('input[type="password"]').fill(password)
    await page.getByRole('button', { name: /登\s*录/ }).click()
    await page.waitForURL(/\/dashboard/, { timeout: 15_000 })
    await page.goto('/edge-device/1')
  }

  async function operation(page: Page, buttonName: string, params: Record<string, unknown> = {}, result: { name: string; value: number; unit: string } | Array<{ name: string; value: number; unit: string }>) {
    const button = page.getByRole('button', { name: buttonName, exact: true })
    await expect(button).toBeVisible({ timeout: 20_000 })
    await expect(button).toBeEnabled()
    const createResponsePromise = page.waitForResponse(response =>
      response.request().method() === 'POST' && /\/api\/v1\/edge-devices\/1\/operations$/.test(response.url()),
    )
    await button.click()

    if (Object.keys(params).length > 0) {
      const form = page.locator('.el-dialog:visible').last()
      for (const [name, value] of Object.entries(params)) {
        const item = form.locator('.el-form-item').filter({ hasText: name })
        if (typeof value === 'string') {
          await item.locator('.el-select').click()
          await page.getByRole('option', { name: value, exact: true }).click()
        } else {
          await item.locator('input').fill(String(value))
        }
      }
      await form.getByRole('button', { name: '继续', exact: true }).click()
    }

    if (Array.isArray(result) ? result.some(item => item.name === 'reset_ack') : result.name === 'write_ack') {
      const confirmation = page.locator('.el-dialog:visible').last()
      await confirmation.getByLabel('操作理由').fill(`SN-3001 实机验证：${buttonName} 后读回并恢复`)
      await confirmation.getByRole('button', { name: '确认并排队', exact: true }).click()
    }

    // A login within the recent-auth window proceeds directly. If the test is
    // resumed after that window, use the same explicit re-authentication path
    // as the production UI rather than bypassing confirmation.
    const reauth = page.getByRole('dialog', { name: '验证当前身份' })
    if (await reauth.isVisible().catch(() => false)) {
      await reauth.getByLabel('当前账户密码').fill(password)
      await reauth.getByRole('button', { name: '验证并继续', exact: true }).click()
    }

    const createResponse = await createResponsePromise
    expect(createResponse.status()).toBe(202)
    const commandID = (await createResponse.json())?.data?.execution?.command_id as string
    expect(commandID).toMatch(/^[0-9a-f-]{36}$/)
    const token = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token') || '')
    const expectedResult = Array.isArray(result) ? result : [result]
    await expect.poll(async () => page.evaluate(async ({ id, authToken }) => {
      const response = await fetch(`/api/v1/device-operations/${id}`, { headers: { Authorization: `Bearer ${authToken}` } })
      const body = await response.json()
      return { status: body?.data?.status, result: body?.data?.verified_result }
    }, { id: commandID, authToken: token }), { timeout: 30_000 }).toEqual({ status: 'SUCCEEDED', result: expectedResult })
    await expect.poll(async () => {
      const timeline = page.locator('.el-timeline-item').first()
      return await timeline.textContent()
    }, { timeout: 20_000 }).toContain('SUCCEEDED')
    const newest = page.locator('.el-timeline-item').first()
    await expect(newest).toContainText('SUCCEEDED')
    for (const item of (Array.isArray(result) ? result : [result])) {
      await expect(newest).toContainText(`${item.name}=${item.value}${item.unit}`)
    }

    // Return the command id through the REST view as an additional durable
    // assertion; the visible timeline is the browser-facing evidence.
    const history = await page.evaluate(async authToken => {
      const response = await fetch('/api/v1/edge-devices/1/operations', { headers: { Authorization: `Bearer ${authToken}` } })
      return (await response.json())?.data || []
    }, token)
    expect(history[0]?.status).toBe('SUCCEEDED')
    return history[0]
  }

  async function waitForChannelBaud(page: Page, expected: number) {
    const token = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token') || '')
    await expect.poll(async () => page.evaluate(async ({ authToken }) => {
      const headers = { Authorization: `Bearer ${authToken}` }
      const [channelsResponse, nodeResponse] = await Promise.all([
        fetch('/api/v1/nodes/1/channels', { headers }),
        fetch('/api/v1/nodes/1', { headers }),
      ])
      const channels = await channelsResponse.json()
      const node = await nodeResponse.json()
      const channel = (channels?.data || []).find((item: any) => item.id === 1)
      const text = String(channel?.bus_config || '').replace(/^\\x|^0x/i, '')
      const baud = /^[0-9a-f]+$/i.test(text) && text.length >= 12 ? Number.parseInt(text.slice(4, 12), 16) : 0
      return { baud, sync: node?.data?.config_sync_state }
    }, { authToken: token }), { timeout: 45_000 }).toEqual({ baud: expected, sync: 'in_sync' })
  }

  async function waitForEdgeAddress(page: Page, expected: number) {
    const token = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token') || '')
    await expect.poll(async () => page.evaluate(async ({ authToken }) => {
      const response = await fetch('/api/v1/edge-devices/1', { headers: { Authorization: `Bearer ${authToken}` } })
      const body = await response.json()
      return { address: String(body?.data?.hardware_id || ''), sync: body?.data?.node?.config_sync_state }
    }, { authToken: token }), { timeout: 45_000 }).toEqual({ address: String(expected), sync: 'in_sync' })
    // ConfigResult can race the first in_sync status report; leave one full
    // status interval before issuing the next physical request.
    await page.waitForTimeout(2_000)
  }

  test('清零写入 ACK、雨量读回，以及参数写入 ACK 后恢复基线', async ({ page }) => {
    test.setTimeout(120_000)
    await login(page)

    await operation(page, '清零累计雨量', {}, [
      { name: 'rainfall', value: 0, unit: 'mm' },
      { name: 'reset_ack', value: 1, unit: 'ack' },
    ])
    // The protocol's raw FC06 clear is also exposed in development as a
    // separately audited action. It must be followed by a browser readback;
    // production uses the bounded reset action above instead.
    await operation(page, '发送雨量清零', {}, { name: 'write_ack', value: 1, unit: 'ack' })
    await operation(page, '读取累计雨量', {}, { name: 'rainfall', value: 0, unit: 'mm' })
    await page.getByRole('button', { name: '读取累计雨量', exact: true }).click()
    await expect.poll(async () => page.locator('.el-timeline-item').first().textContent(), { timeout: 20_000 }).toContain('rainfall=0mm')

    // The device reports a real baseline of 50 (the protocol's documented
    // default is 60). Write 51, read it back, then restore 50 and read again.
    await operation(page, '设置雨量灵敏度', { value: 51 }, { name: 'write_ack', value: 1, unit: 'ack' })
    await operation(page, '读取雨量灵敏度', {}, { name: 'rain_sensitivity', value: 51, unit: 'raw' })
    await operation(page, '设置雨量灵敏度', { value: 50 }, { name: 'write_ack', value: 1, unit: 'ack' })
    await operation(page, '读取雨量灵敏度', {}, { name: 'rain_sensitivity', value: 50, unit: 'raw' })

    // Address and baud are written to their current values here. This proves
    // the real device ACK and readback without changing the C6 UART manifest.
    await operation(page, '设置设备地址', { value: 1 }, { name: 'write_ack', value: 1, unit: 'ack' })
    await operation(page, '读取设备地址', {}, { name: 'device_address', value: 1, unit: 'address' })

    // Exercise the address transition itself, using source_address on the
    // second write so the recovery request reaches the new address.
    await operation(page, '设置设备地址', { value: 2 }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForEdgeAddress(page, 2)
    await operation(page, '读取设备地址', {}, { name: 'device_address', value: 2, unit: 'address' })
    await operation(page, '设置设备地址', { value: 1, source_address: 2 }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForEdgeAddress(page, 1)
    await operation(page, '读取设备地址', {}, { name: 'device_address', value: 1, unit: 'address' })

    await operation(page, '设置设备波特率', { value: '4800' }, { name: 'write_ack', value: 1, unit: 'ack' })
    await operation(page, '读取设备波特率', {}, { name: 'baud_rate', value: 4800, unit: 'bit/s' })

    await page.screenshot({ path: '/tmp/e2e-shots/sn3001-real-writes.png', fullPage: true })
  })

  test('单独清零为重启回放准备持久命令', async ({ page }) => {
    test.setTimeout(60_000)
    await login(page)
    await operation(page, '清零累计雨量', {}, [
      { name: 'rainfall', value: 0, unit: 'mm' },
      { name: 'reset_ack', value: 1, unit: 'ack' },
    ])
  })

  test('真实切换 4800→9600→4800 并在新速率读回', async ({ page }) => {
    test.setTimeout(150_000)
    await login(page)

    await operation(page, '设置设备波特率', { value: '9600' }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForChannelBaud(page, 9600)
    await operation(page, '读取设备波特率', {}, { name: 'baud_rate', value: 9600, unit: 'bit/s' })

    await operation(page, '设置设备波特率', { value: '4800' }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForChannelBaud(page, 4800)
    await operation(page, '读取设备波特率', {}, { name: 'baud_rate', value: 4800, unit: 'bit/s' })
    await page.screenshot({ path: '/tmp/e2e-shots/sn3001-real-baud-transition.png', fullPage: true })
  })

  test('真实切换 4800→2400→4800 并在新速率读回', async ({ page }) => {
    test.setTimeout(150_000)
    await login(page)

    await operation(page, '设置设备波特率', { value: '2400' }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForChannelBaud(page, 2400)
    await operation(page, '读取设备波特率', {}, { name: 'baud_rate', value: 2400, unit: 'bit/s' })

    await operation(page, '设置设备波特率', { value: '4800' }, { name: 'write_ack', value: 1, unit: 'ack' })
    await waitForChannelBaud(page, 4800)
    await operation(page, '读取设备波特率', {}, { name: 'baud_rate', value: 4800, unit: 'bit/s' })
  })
})
