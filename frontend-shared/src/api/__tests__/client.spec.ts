import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// hoisted: factory 内部可以使用
const { mockRequestHandler, mockResponseFulfilled, mockResponseRejected } = vi.hoisted(() => ({
  mockRequestHandler: vi.fn(),
  mockResponseFulfilled: vi.fn(),
  mockResponseRejected: vi.fn(),
}))

vi.mock('axios', () => {
  const instance = {
    interceptors: {
      request: { use: (ok: any, _err: any) => mockRequestHandler.mockImplementation(ok) },
      response: { use: (ok: any, err: any) => {
        mockResponseFulfilled.mockImplementation(ok)
        mockResponseRejected.mockImplementation(err)
      } },
    },
  }
  return {
    default: { create: () => instance, ...instance },
    create: () => instance,
  }
})

// import 在 mock 之后
import apiClient from '../client'
void apiClient // 触发模块加载 + 拦截器注册

describe('apiClient 拦截器', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('请求时自动附加 Authorization Bearer Token (from localStorage)', () => {
    localStorage.setItem('token', 'test-jwt-123')
    const handler = mockRequestHandler.getMockImplementation() as any
    expect(handler).toBeDefined()
    const config = { headers: {} as any }
    const result = handler(config)
    expect(result.headers.Authorization).toBe('Bearer test-jwt-123')
  })

  it('请求时也读取 sessionStorage 中的 Token', () => {
    sessionStorage.setItem('token', 'session-jwt-456')
    const handler = mockRequestHandler.getMockImplementation() as any
    const config = { headers: {} as any }
    const result = handler(config)
    expect(result.headers.Authorization).toBe('Bearer session-jwt-456')
  })

  it('localStorage 优先于 sessionStorage', () => {
    localStorage.setItem('token', 'local-priority')
    sessionStorage.setItem('token', 'session-fallback')
    const handler = mockRequestHandler.getMockImplementation() as any
    const config = { headers: {} as any }
    const result = handler(config)
    expect(result.headers.Authorization).toBe('Bearer local-priority')
  })

  it('没有 Token 时不附加 Authorization', () => {
    const handler = mockRequestHandler.getMockImplementation() as any
    const config = { headers: {} as any }
    const result = handler(config)
    expect(result.headers.Authorization).toBeUndefined()
  })

  it('业务 code 非 200 时 reject with message', async () => {
    const handler = mockResponseFulfilled.getMockImplementation() as any
    await expect(handler({ data: { code: 400, message: '参数错误' } })).rejects.toThrow('参数错误')
  })

  it('业务 code 200 时 resolve data', () => {
    const handler = mockResponseFulfilled.getMockImplementation() as any
    const data = { code: 200, message: 'ok', data: { foo: 1 } }
    expect(handler({ data })).toEqual(data)
  })

  it('业务 code 缺失时 resolve data (向后兼容)', () => {
    const handler = mockResponseFulfilled.getMockImplementation() as any
    const data = { foo: 'bar' }
    expect(handler({ data })).toEqual(data)
  })

  it('HTTP 401 时清除 token 并跳 /login', async () => {
    localStorage.setItem('token', 'expired')
    sessionStorage.setItem('token', 'expired-session')

    // jsdom 不允许直接赋值 location.href, 用 defineProperty mock
    const originalLocation = window.location
    delete (window as any).location
    ;(window as any).location = { ...originalLocation, href: '' }

    const handler = mockResponseRejected.getMockImplementation() as any
    await expect(handler({ response: { status: 401 } })).rejects.toBeDefined()

    expect(localStorage.getItem('token')).toBeNull()
    expect(sessionStorage.getItem('token')).toBeNull()
    expect(window.location.href).toBe('/login')

    // 还原
    ;(window as any).location = originalLocation
  })

  it('非 401 HTTP 错误不跳 /login', async () => {
    const originalLocation = window.location
    delete (window as any).location
    ;(window as any).location = { ...originalLocation, href: '' }

    localStorage.setItem('token', 'still-valid')
    const handler = mockResponseRejected.getMockImplementation() as any
    await expect(handler({ response: { status: 500, data: { message: 'Server Error' } } })).rejects.toThrow('Server Error')
    expect(localStorage.getItem('token')).toBe('still-valid')
    expect(window.location.href).toBe('')
    ;(window as any).location = originalLocation
  })
})
