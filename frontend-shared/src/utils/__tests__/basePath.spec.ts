import { describe, it, expect, afterEach, vi } from 'vitest'
import { basePrefix, withBase, loginPath } from '../basePath'

describe('basePath 工具（子路径前缀部署）', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it('无前缀时 basePrefix 返回空串', () => {
    vi.stubEnv('VITE_BASE_PATH', '')
    expect(basePrefix()).toBe('')
  })

  it('默认（未配置）时 basePrefix 返回空串', () => {
    vi.stubEnv('VITE_BASE_PATH', undefined)
    expect(basePrefix()).toBe('')
  })

  it('VITE_BASE_PATH=/ 时 basePrefix 返回空串（根路径部署）', () => {
    vi.stubEnv('VITE_BASE_PATH', '/')
    expect(basePrefix()).toBe('')
  })

  it('VITE_BASE_PATH=/ehome-dev/ 时 basePrefix 去掉尾部斜杠', () => {
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    expect(basePrefix()).toBe('/ehome-dev')
  })

  it('withBase 无前缀时原样返回', () => {
    vi.stubEnv('VITE_BASE_PATH', '')
    expect(withBase('/login')).toBe('/login')
  })

  it('withBase 带前缀时拼接前缀', () => {
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    expect(withBase('/login')).toBe('/ehome-dev/login')
  })

  it('withBase 对已带前缀的路径不重复拼接', () => {
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    expect(withBase('/ehome-dev/dashboard')).toBe('/ehome-dev/ehome-dev/dashboard')
  })

  it('loginPath 无前缀时返回 /login', () => {
    vi.stubEnv('VITE_BASE_PATH', '')
    expect(loginPath()).toBe('/login')
  })

  it('loginPath 带前缀时返回 /ehome-dev/login', () => {
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    expect(loginPath()).toBe('/ehome-dev/login')
  })
})
