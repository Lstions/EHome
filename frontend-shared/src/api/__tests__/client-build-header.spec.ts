import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * 通道 1：响应头 `X-EHome-Build`（契约 §4.3 / AC-13）。
 *
 * 活跃用户感知更新的延迟 = 下一次 API 响应。这条通道若失效，用户会一直用着旧页面，
 * 直到某次点击撞上已不存在的 chunk（那时只能出不可推迟的模态，体验更差）。
 */

const mocks = vi.hoisted(() => ({
  fulfilled: vi.fn(),
  rejected: vi.fn(),
  notifyServerBuild: vi.fn(),
  clearSessionCaches: vi.fn(),
}))

vi.mock('@/utils/sessionCache', () => ({ clearSessionCaches: mocks.clearSessionCaches }))
vi.mock('@/stores/appUpdate', () => ({ notifyServerBuild: mocks.notifyServerBuild }))

vi.mock('axios', () => {
  const instance = {
    interceptors: {
      request: { use: vi.fn() },
      response: {
        use: (ok: unknown, err: unknown) => {
          mocks.fulfilled.mockImplementation(ok as never)
          mocks.rejected.mockImplementation(err as never)
        },
      },
    },
    defaults: { baseURL: '' },
  }
  return { default: { create: () => instance }, create: () => instance }
})

import '../client'

function handle(response: Record<string, unknown>) {
  const handler = mocks.fulfilled.getMockImplementation() as (r: unknown) => unknown
  expect(typeof handler).toBe('function')
  return handler(response)
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

describe('apiClient 响应头通道', () => {
  it('普通小写键响应头 → 投递服务端 build', () => {
    handle({ data: { code: 200, data: null }, headers: { 'x-ehome-build': 'server-build-2' } })
    expect(mocks.notifyServerBuild).toHaveBeenCalledWith('server-build-2')
  })

  it('AxiosHeaders 形态（有 get 方法）→ 同样能读到', () => {
    const headers = { get: (name: string) => (name.toLowerCase() === 'x-ehome-build' ? 'server-build-3' : undefined) }
    handle({ data: { code: 200, data: null }, headers })
    expect(mocks.notifyServerBuild).toHaveBeenCalledWith('server-build-3')
  })

  it('响应头缺失（部署窗口 502/503）→ 不投递、不抛错', () => {
    expect(() => handle({ data: { code: 200, data: null } })).not.toThrow()
    expect(mocks.notifyServerBuild).not.toHaveBeenCalled()
  })

  it('空字符串头 → 不投递', () => {
    handle({ data: { code: 200, data: null }, headers: { 'x-ehome-build': '   ' } })
    expect(mocks.notifyServerBuild).not.toHaveBeenCalled()
  })

  it('比对在解包之前完成，且返回值仍是 response.data（既有契约不变）', () => {
    const payload = { code: 200, message: 'ok', data: { items: [1, 2] } }
    const result = handle({ data: payload, headers: { 'x-ehome-build': 'server-build-4' } })
    expect(result).toBe(payload)
    expect(mocks.notifyServerBuild).toHaveBeenCalledWith('server-build-4')
  })

  it('业务 code >= 400 仍按业务错误 reject（不因读头而改变）', async () => {
    const handler = mocks.fulfilled.getMockImplementation() as (r: unknown) => Promise<unknown>
    await expect(handler({ data: { code: 409, message: '冲突', error_code: 'FIRMWARE_IN_USE' }, headers: {} }))
      .rejects.toThrow('冲突')
  })
})
