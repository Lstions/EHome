import { describe, expect, it, vi, beforeEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { stripComments } from '@/utils/__tests__/sourceScan'

/**
 * 维护态接口（api/system.ts）—— 三条**安全边界**的合同测试。
 *
 * 为什么这个文件存在（而不是只测 App 门禁）：
 * 门禁的单测里 `@/api/system` 是被 mock 掉的，因此「独立 axios 实例」这个**最关键**的
 * 约束在那里完全没有被验证 —— 而它恰恰是本次改动里最容易被人"顺手优化"掉的一行：
 * 一旦改成复用 `api/client.ts` 的 apiClient，维护期返回 401 时全局拦截器会
 * 清 session 并硬跳 `/login`，维护页永远不可达（未登录是维护期的正常态）。
 */

// 用「配置盒子」而不是 mock.calls 记录：mock 工厂在模块图求值时就会执行一次，
// 而不同 vitest worker 对该时机与记录可见性的处理并不一致（实测 mock.calls 恒为 0，
// 用例假红）。盒子写入不依赖任何 mock 记录机制，断言稳定。
const { mockGet, state } = vi.hoisted(() => ({
  mockGet: vi.fn(),
  state: { createCalls: 0, config: null as null | Record<string, unknown> },
}))

vi.mock('axios', () => {
  const instance = { get: mockGet, defaults: {} }
  const create = (config: Record<string, unknown>) => {
    state.createCalls += 1
    state.config = config
    return instance
  }
  return { __esModule: true, default: { create }, create }
})

import {
  MAINTENANCE_TIMEOUT_MS,
  MaintenanceUnsupportedError,
  getMaintenanceStatus,
  isMaintenanceSnapshot,
  normalizeSnapshot,
} from '../system'

const validSnapshot = {
  ready: false,
  failed: false,
  percent: 42,
  current_step: 'parent_up',
  current_label: '补齐父表索引',
  started_at: '2026-11-06T08:00:00Z',
  elapsed_ms: 3000,
  steps: [],
  version: '2.0.0',
}

describe('维护态客户端：必须独立于全局 apiClient', () => {
  it('通过 axios.create 建实例，超时 5s', () => {
    expect(state.createCalls, '模块加载时必须用 axios.create 建独立实例').toBe(1)
    expect(state.config?.timeout).toBe(MAINTENANCE_TIMEOUT_MS)
    expect(MAINTENANCE_TIMEOUT_MS).toBe(5000)
  })

  it('源码层面不 import 全局 api/client、不注册拦截器', () => {
    // 与仓内既有门禁同口径：process.cwd() 定位（vitest 在 frontend-shared 下运行）。
    // 不能用 import.meta.url —— vitest 里它不是 file: 协议，new URL 会抛 "must be of scheme file"。
    // 必须先剥注释：本文件的说明性注释里会出现 apiClient / api/client 作为反例引用。
    const source = stripComments(readFileSync(join(process.cwd(), 'src/api/system.ts'), 'utf8'))
    expect(source).not.toMatch(/from '\.\/client'/)
    expect(source).not.toContain('apiClient')
    expect(source).not.toContain('.interceptors')
    // 反向自证：确实读到了目标文件（防路径写错导致上面的 not 断言恒真）
    expect(source).toContain('getMaintenanceStatus')
    expect(source).toContain('axios.create')
  })
})

describe('isMaintenanceSnapshot：fail-closed 快照校验', () => {
  it('合法快照', () => {
    expect(isMaintenanceSnapshot(validSnapshot)).toBe(true)
  })

  it('缺少 ready 一律判非法（ready 是门禁唯一决策依据，缺了就不能猜）', () => {
    expect(isMaintenanceSnapshot(null)).toBe(false)
    expect(isMaintenanceSnapshot({})).toBe(false)
    expect(isMaintenanceSnapshot({ ready: 'true', steps: [] })).toBe(false)
    expect(isMaintenanceSnapshot({ steps: [] })).toBe(false)
    expect(isMaintenanceSnapshot('<!doctype html>')).toBe(false)
  })

  it('steps 缺失 / null 仍判合法（Go 的 nil slice 序列化成 null，不能因此 fail-open 进业务页）', () => {
    // var steps []Step 为 nil 时 json.Marshal 得到 "steps":null。
    // 若判非法 → 门禁 fail-open 直接进业务页 → 维护期用户撞满 503，是最坏结果。
    expect(isMaintenanceSnapshot({ ready: false })).toBe(true)
    expect(isMaintenanceSnapshot({ ready: false, steps: null })).toBe(true)
    expect(isMaintenanceSnapshot({ ready: true, steps: null })).toBe(true)
  })

  it('steps 存在但不是数组 → 非法（那是真的形状不对）', () => {
    expect(isMaintenanceSnapshot({ ready: false, steps: 'oops' })).toBe(false)
    expect(isMaintenanceSnapshot({ ready: false, steps: {} })).toBe(false)
  })
})

describe('normalizeSnapshot：Go 零值 → TS 严格类型的归一', () => {
  it('steps:null → []（否则 v-for 渲染 null 会抛错）', () => {
    const out = normalizeSnapshot({ ...validSnapshot, steps: null as unknown as [] })
    expect(out.steps).toEqual([])
  })

  it('缺失的 percent / elapsed_ms / current_* 归一到安全默认值', () => {
    const raw = {
      ready: false,
      failed: false,
      percent: undefined as unknown as number,
      current_step: undefined as unknown as string,
      current_label: undefined as unknown as string,
      started_at: '',
      elapsed_ms: undefined as unknown as number,
      steps: null as unknown as [],
      version: 'x',
    }
    const out = normalizeSnapshot(raw)
    expect(out.percent).toBe(0)
    expect(out.elapsed_ms).toBe(0)
    expect(out.current_step).toBe('')
    expect(out.current_label).toBe('')
    expect(out.steps).toEqual([])
  })

  it('步骤内的 progress / weight 缺失 → 0（不产生 NaN%）', () => {
    const out = normalizeSnapshot({
      ...validSnapshot,
      steps: [{ id: 'a', label: 'A', state: 'running' } as unknown as never],
    })
    expect(out.steps[0].progress).toBe(0)
    expect(out.steps[0].weight).toBe(0)
  })

  it('合法快照归一后语义不变（不误改数据）', () => {
    const out = normalizeSnapshot({ ...validSnapshot, steps: [{ id: 'a', label: 'A', weight: 3, state: 'done', progress: 100 }] })
    expect(out.percent).toBe(validSnapshot.percent)
    expect(out.steps[0].progress).toBe(100)
    expect(out.steps[0].weight).toBe(3)
  })
})

describe('getMaintenanceStatus：成功/异常分类', () => {
  beforeEach(() => {
    mockGet.mockReset()
  })

  it('code=200 → 返回 data 快照', async () => {
    mockGet.mockResolvedValue({ data: { code: 200, message: 'ok', data: validSnapshot } })
    await expect(getMaintenanceStatus()).resolves.toEqual(validSnapshot)
    expect(mockGet).toHaveBeenCalledWith('/api/v1/system/maintenance')
  })

  it('code>=400 → 抛普通错误（业务错误，交给门禁按失败处理）', async () => {
    mockGet.mockResolvedValue({ data: { code: 500, message: 'boom', data: null } })
    await expect(getMaintenanceStatus()).rejects.toThrow('boom')
  })

  it('HTTP 404 → MaintenanceUnsupportedError（旧后端/静态托管，门禁要 fail-open）', async () => {
    mockGet.mockRejectedValue(Object.assign(new Error('Not Found'), { response: { status: 404 } }))
    await expect(getMaintenanceStatus()).rejects.toBeInstanceOf(MaintenanceUnsupportedError)
  })

  it('响应是 HTML（纯静态服务器把 /api/* 回退到 index.html）→ MaintenanceUnsupportedError', async () => {
    mockGet.mockResolvedValue({ data: '<!doctype html><html></html>' })
    await expect(getMaintenanceStatus()).rejects.toBeInstanceOf(MaintenanceUnsupportedError)
  })

  it('data 不是合法快照（旧后端返回了别的形状）→ MaintenanceUnsupportedError', async () => {
    mockGet.mockResolvedValue({ data: { code: 200, message: 'ok', data: { status: 'ok' } } })
    await expect(getMaintenanceStatus()).rejects.toBeInstanceOf(MaintenanceUnsupportedError)
  })

  it('steps 为 null 的正常快照（Go nil slice）→ 成功返回且 steps 归一成 []', async () => {
    mockGet.mockResolvedValue({
      data: { code: 200, message: 'ok', data: { ...validSnapshot, steps: null } },
    })
    const result = await getMaintenanceStatus()
    expect(result.steps).toEqual([])
    expect(result.ready).toBe(false)
  })

  it('网络错误 / 超时原样上抛（门禁进入"连接中"并继续轮询，不能伪装成未维护）', async () => {
    const err = Object.assign(new Error('timeout of 5000ms exceeded'), { code: 'ECONNABORTED' })
    mockGet.mockRejectedValue(err)
    await expect(getMaintenanceStatus()).rejects.toBe(err)
  })

  it('503（维护期业务响应）也原样上抛，由门禁决定重试', async () => {
    const err = Object.assign(new Error('maintenance'), { response: { status: 503 } })
    mockGet.mockRejectedValue(err)
    await expect(getMaintenanceStatus()).rejects.toBe(err)
  })
})
