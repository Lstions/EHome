import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * `GET /api/v1/system/version` 的前端消费层（契约 §4.3 / §6.5 / AC-18）。
 *
 * 这个端点是「旧页面是否过期」与「本次改了什么」的唯一数据源，两种失败方式都必须被区分：
 *   · 后端**不认识**该端点（旧镜像 / 静态托管）⇒ 抛 UnsupportedError，UI 保持旧行为；
 *   · 后端**暂时连不上**（部署窗口 502/503）⇒ 原样抛出，由 store 维持现状。
 * 把两者都当成「没有更新」会静默放过过期页面；都当成错误会刷满日志。
 */

const mocks = vi.hoisted(() => ({
  createCount: 0,
  get: vi.fn(),
}))

vi.mock('axios', () => {
  const instance = {
    get: mocks.get,
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
    interceptors: {
      request: { use: vi.fn() },
      response: { use: vi.fn() },
    },
  }
  return {
    default: {
      create: () => {
        mocks.createCount += 1
        return instance
      },
    },
    create: () => {
      mocks.createCount += 1
      return instance
    },
  }
})

import {
  getSystemVersion,
  normalizeVersionInfo,
  SystemVersionUnsupportedError,
} from '../system'

function envelope(data: unknown, code = 200) {
  return { data: { code, message: 'ok', data } }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('normalizeVersionInfo', () => {
  const raw = {
    version: '2.9.0',
    build_id: 'a1b2c3d',
    commit: 'a1b2c3d9e8f7',
    build_time: '2026-11-18T06:12:00Z',
    notes: ['固件上传改为必填 sidecar'],
  }

  it('完整快照原样返回', () => {
    expect(normalizeVersionInfo(raw)).toEqual(raw)
  })

  it('notes 为 null / 缺失 / 非数组 → []（契约：不省略、不 null，前端同等处理）', () => {
    expect(normalizeVersionInfo({ ...raw, notes: null })?.notes).toEqual([])
    expect(normalizeVersionInfo({ version: '2.9.0', build_id: 'x' })?.notes).toEqual([])
    expect(normalizeVersionInfo({ ...raw, notes: 'oops' })?.notes).toEqual([])
  })

  it('剔除空串与非字符串条目（避免渲染空白行）', () => {
    expect(normalizeVersionInfo({ ...raw, notes: ['有效', '', '  ', 3, null] })?.notes).toEqual(['有效'])
  })

  it('既无 version 也无 build_id → null（认不出就 fail-closed）', () => {
    expect(normalizeVersionInfo({})).toBeNull()
    expect(normalizeVersionInfo(null)).toBeNull()
    expect(normalizeVersionInfo('html')).toBeNull()
  })
})

describe('getSystemVersion', () => {
  it('成功：解包 envelope 并归一 notes', async () => {
    mocks.get.mockResolvedValue(envelope({
      version: '2.9.0',
      build_id: 'a1b2c3d',
      commit: 'abc',
      build_time: '2026-11-18T06:12:00Z',
      notes: ['一条'],
    }))
    const info = await getSystemVersion()
    expect(info.version).toBe('2.9.0')
    expect(info.build_id).toBe('a1b2c3d')
    expect(info.notes).toEqual(['一条'])
    expect(mocks.get).toHaveBeenCalledWith('/api/v1/system/version')
  })

  it('HTTP 404 → UnsupportedError（旧后端不认识该端点）', async () => {
    mocks.get.mockRejectedValue({ response: { status: 404 } })
    await expect(getSystemVersion()).rejects.toBeInstanceOf(SystemVersionUnsupportedError)
  })

  it('非 envelope（纯静态服务器回 SPA 的 index.html）→ UnsupportedError', async () => {
    mocks.get.mockResolvedValue({ data: '<!DOCTYPE html><html></html>' })
    await expect(getSystemVersion()).rejects.toBeInstanceOf(SystemVersionUnsupportedError)
  })

  it('envelope code >= 400 → 普通 Error（不是「不支持」）', async () => {
    mocks.get.mockResolvedValue({ data: { code: 503, message: '维护中', data: null } })
    await expect(getSystemVersion()).rejects.toThrow('维护中')
  })

  it('data 形状不对 → UnsupportedError', async () => {
    mocks.get.mockResolvedValue(envelope({ hello: 'world' }))
    await expect(getSystemVersion()).rejects.toBeInstanceOf(SystemVersionUnsupportedError)
  })

  it('网络错误原样抛出（由 store 决定「维持现状」）', async () => {
    mocks.get.mockRejectedValue(new Error('connect ECONNREFUSED'))
    await expect(getSystemVersion()).rejects.toThrow('ECONNREFUSED')
  })

  it('复用维护态实例：模块加载不额外 create axios 实例（避免多一份拦截器风险）', async () => {
    mocks.get.mockResolvedValue(envelope({ version: '1.0.0', build_id: 'x' }))
    await getSystemVersion()
    expect(mocks.createCount).toBe(1)
  })
})
