import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/**
 * routeProgress 单例行为测试（fake timers）。
 * 覆盖：start 推进 / done 收尾隐藏 / fail 收敛 / reset 立即复位 / 防重入。
 */

// 必须重置模块级单例：每次重新 import 拿到全新状态
const importStore = () => import('@/stores/routeProgress')
let mod: Awaited<ReturnType<typeof importStore>>
let api: ReturnType<typeof mod.useRouteProgress>
let state: ReturnType<typeof mod.__routeProgressState>

describe('stores/routeProgress', () => {
  beforeEach(async () => {
    vi.useFakeTimers()
    mod = await importStore()
    api = mod.useRouteProgress()
    state = mod.__routeProgressState()
    // 清掉上一个用例可能残留的定时器
    api.reset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('初始为非 busy 且隐藏', () => {
    expect(api.isBusy()).toBe(false)
    expect(state.visible.value).toBe(false)
    expect(state.progress.value).toBe(0)
  })

  it('start 后 visible 且进度从 0 开始推进（tick 衰减，不越过 85% 上限）', () => {
    api.start()
    expect(api.isBusy()).toBe(true)
    expect(state.visible.value).toBe(true)
    // 第一个 tick：0 < 30 → +8
    vi.advanceTimersByTime(200)
    expect(state.progress.value).toBe(8)
    // 30-60 区间 +4
    vi.advanceTimersByTime(200 * 8) // 8+4*7=36 → 44
    expect(state.progress.value).toBeGreaterThan(30)
    expect(state.progress.value).toBeLessThan(60)
    // 跑到 90s 后仍不应超过 SOFT_CAP=85
    vi.advanceTimersByTime(200 * 200)
    expect(state.progress.value).toBeLessThanOrEqual(85)
    expect(state.visible.value).toBe(true)
  })

  it('done 收满并 250ms 后隐藏（非 busy 状态下 done 被忽略）', () => {
    api.start()
    vi.advanceTimersByTime(200)
    api.done()
    // done 立即收满
    expect(state.progress.value).toBe(100)
    expect(state.visible.value).toBe(true) // 250ms 隐藏延迟尚未到
    expect(api.isBusy()).toBe(true)
    vi.advanceTimersByTime(250)
    expect(state.visible.value).toBe(false)
    expect(state.progress.value).toBe(0)
    expect(api.isBusy()).toBe(false)
  })

  it('done 在非 busy 时不干扰状态', () => {
    api.done()
    expect(api.isBusy()).toBe(false)
    expect(state.visible.value).toBe(false)
    expect(state.progress.value).toBe(0)
  })

  it('fail 与 done 同样收敛（不卡死）', () => {
    api.start()
    vi.advanceTimersByTime(200)
    api.fail()
    expect(state.progress.value).toBe(100)
    expect(api.isBusy()).toBe(true)
    vi.advanceTimersByTime(250)
    expect(state.visible.value).toBe(false)
    expect(api.isBusy()).toBe(false)
  })

  it('reset 立即复位（过渡层强制退出路径）', () => {
    api.start()
    vi.advanceTimersByTime(200)
    api.reset()
    expect(state.visible.value).toBe(false)
    expect(state.progress.value).toBe(0)
    expect(api.isBusy()).toBe(false)
    // reset 后不应再有 tick 推进
    vi.advanceTimersByTime(500)
    expect(state.progress.value).toBe(0)
  })

  it('防重入：busy 期间再次 start 不重置进度', () => {
    api.start()
    vi.advanceTimersByTime(400) // 进度 8+8=16
    const before = state.progress.value
    api.start()
    expect(state.progress.value).toBe(before)
    // 防重入不清 timer：tick 仍继续
    vi.advanceTimersByTime(200)
    expect(state.progress.value).toBeGreaterThan(before)
  })
})
