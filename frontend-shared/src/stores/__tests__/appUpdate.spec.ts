import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

/**
 * 旧页面更新检测 store（契约 §6.4：4 态 + 四通道）。
 *
 * 这一组用例刻意按「状态机 → 通道 → 边界」组织，因为方案的验收是**逐条**的：
 *   AC-13：活跃用户下一次 API 响应即提示；空闲用户 ≤35 分钟；回滚不提示；「稍后」不再提示；
 *   AC-16：chunk 加载失败立即出不可推迟的模态；
 *   AC-18：notes 为空时两处都不出现入口。
 * 所以每条通道都要有一条「它自己会触发」的用例，否则四条通道里死掉一条也不会变红。
 */

const mocks = vi.hoisted(() => ({
  getSystemVersion: vi.fn(),
  onConnected: vi.fn(),
  handlers: [] as Array<() => void>,
}))

vi.mock('@/api/system', () => {
  class SystemVersionUnsupportedError extends Error {
    constructor(message: string) {
      super(message)
      this.name = 'SystemVersionUnsupportedError'
    }
  }
  return { getSystemVersion: mocks.getSystemVersion, SystemVersionUnsupportedError }
})

vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({
    onConnected: (handler: () => void) => {
      mocks.handlers.push(handler)
      return () => {
        const index = mocks.handlers.indexOf(handler)
        if (index >= 0) mocks.handlers.splice(index, 1)
      }
    },
  }),
}))

import {
  APP_UPDATE_DISMISS_KEY,
  APP_UPDATE_MIN_GAP_MS,
  APP_UPDATE_POLL_INTERVAL_MS,
  isModuleLoadFailure,
  notifyModuleLoadFailure,
  notifyServerBuild,
  resetAppUpdateBridgeForTests,
  useAppUpdateStore,
} from '../appUpdate'

const LOCAL_BUILD = 'build-local-aaa'
const LOCAL_TIME = '2026-01-01T00:00:00Z'
const SERVER_TIME_NEWER = '2026-02-01T00:00:00Z'
const SERVER_TIME_OLDER = '2025-12-01T00:00:00Z'

function versionPayload(patch: Partial<{ version: string; build_id: string; build_time: string; notes: string[] }> = {}) {
  return {
    version: '2.9.0',
    build_id: 'build-server-bbb',
    commit: 'abcdef1234567890',
    build_time: SERVER_TIME_NEWER,
    notes: ['固件上传改为必填 sidecar'],
    ...patch,
  }
}

function makeStore() {
  setActivePinia(createPinia())
  return useAppUpdateStore()
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-03-01T00:00:00Z'))
  vi.clearAllMocks()
  mocks.handlers.length = 0
  resetAppUpdateBridgeForTests()
  sessionStorage.clear()
  vi.stubEnv('VITE_BUILD_ID', LOCAL_BUILD)
  vi.stubEnv('VITE_BUILD_TIME', LOCAL_TIME)
  vi.stubEnv('VITE_APP_VERSION', '')
  mocks.getSystemVersion.mockResolvedValue(versionPayload())
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllEnvs()
  resetAppUpdateBridgeForTests()
})

describe('appUpdate store · 4 态转移', () => {
  it('build_id 相同 → same（不打扰用户）', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('same')
    expect(store.bannerVisible).toBe(false)
    expect(store.modalVisible).toBe(false)
    store.dispose()
  })

  it('build_id 不同且服务端 build_time 更新 → stale（横幅）', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('stale')
    expect(store.bannerVisible).toBe(true)
    expect(store.serverBuildId).toBe('build-server-bbb')
    store.dispose()
  })

  it('部署回滚（服务端 build_time 更旧）→ same，不提示', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_time: SERVER_TIME_OLDER }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('same')
    expect(store.bannerVisible).toBe(false)
    store.dispose()
  })

  it('「稍后」→ dismissed，并按 build_id 记进 sessionStorage（按标签页）', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('stale')

    store.dismiss()
    expect(store.state).toBe('dismissed')
    expect(store.bannerVisible).toBe(false)
    expect(sessionStorage.getItem(APP_UPDATE_DISMISS_KEY)).toBe('build-server-bbb')
    store.dispose()
  })

  it('已 dismissed 的 build 再次检查仍是 dismissed（不主动提示）', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    store.dismiss()
    // 手动再查一次同一 build（越过 5s 最小间隔，确保这次检查真的发出去了）
    await vi.advanceTimersByTimeAsync(APP_UPDATE_MIN_GAP_MS + 1000)
    mocks.getSystemVersion.mockClear()
    mocks.getSystemVersion.mockResolvedValue(versionPayload())
    await store.check('manual')
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)
    expect(store.state).toBe('dismissed')
    store.dispose()
  })

  it('chunk 加载失败 → blocked，且**不可被「稍后」压制**', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    store.dismiss()
    expect(store.state).toBe('dismissed')

    notifyModuleLoadFailure('Failed to fetch dynamically imported module: /assets/NodeList-x.js')
    expect(store.state).toBe('blocked')
    expect(store.modalVisible).toBe(true)

    // blocked 下 dismiss 无效（不可推迟）
    store.dismiss()
    expect(store.state).toBe('blocked')

    // 后续检查也不得把 blocked 降级成 same/stale
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    await store.check('manual')
    expect(store.state).toBe('blocked')
    store.dispose()
  })

  it('检查失败（网络错误）不改变已有状态，也不伪造 same', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('stale')

    // 越过 5s 最小间隔，让这次检查真的发出（否则只是被节流丢弃，断言就假绿了）
    await vi.advanceTimersByTimeAsync(APP_UPDATE_MIN_GAP_MS + 1000)
    mocks.getSystemVersion.mockClear()
    mocks.getSystemVersion.mockRejectedValue(new Error('connect ECONNREFUSED'))
    await store.check('manual')
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)
    expect(store.state).toBe('stale')
    store.dispose()
  })

  it('本地无 build id（开发服务器）→ 永不提示', async () => {
    vi.stubEnv('VITE_BUILD_ID', '')
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('same')
    store.dispose()
  })
})

describe('appUpdate store · 四通道各自触发', () => {
  it('通道 1：响应头里的新 build → 触发检查并进入 stale', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('same')
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    // 部署发生：下一个 /api/* 响应带上了新 build
    await vi.advanceTimersByTimeAsync(6000)
    mocks.getSystemVersion.mockResolvedValue(versionPayload())
    notifyServerBuild('build-server-bbb')
    await vi.advanceTimersByTimeAsync(0)

    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(2)
    expect(store.state).toBe('stale')
    store.dispose()
  })

  it('通道 1 被 min-gap 丢弃 → 5s 边界尾沿补跑一次并进入 stale（E2E-06 根因回归）', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)
    expect(store.state).toBe('same')

    // t≈1.2s：部署发生，响应头带来新 build —— 这次检查落在 5s 最小间隔内，被丢弃
    await vi.advanceTimersByTimeAsync(1200)
    mocks.getSystemVersion.mockResolvedValue(versionPayload())
    notifyServerBuild('build-server-bbb')
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion, '被节流时不得立即发请求').toHaveBeenCalledTimes(1)
    expect(store.state, '尚未到边界，仍维持 same').toBe('same')

    // 同一窗口内再来一个触发：应合并到同一个边界，不额外堆定时器/请求
    await vi.advanceTimersByTimeAsync(800)
    notifyServerBuild('build-server-bbb')
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    // 到 5s 边界：尾沿补跑 ⇒ 用户最终仍会看到更新提示，不会被永久丢弃
    await vi.advanceTimersByTimeAsync(APP_UPDATE_MIN_GAP_MS)
    expect(mocks.getSystemVersion, '边界时刻应补跑一次').toHaveBeenCalledTimes(2)
    expect(store.state).toBe('stale')
    store.dispose()
  })

  it('通道 1：响应头的 build 与本地一致时不发请求（最热路径不得打接口）', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(6000)
    notifyServerBuild(LOCAL_BUILD)
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)
    store.dispose()
  })

  it('通道 2：WebSocket 重连成功 → 触发检查', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)
    expect(mocks.handlers.length).toBe(1)

    await vi.advanceTimersByTimeAsync(6000)
    mocks.getSystemVersion.mockResolvedValue(versionPayload())
    mocks.handlers[0]() // onopen → onConnected 回调
    await vi.advanceTimersByTimeAsync(0)

    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(2)
    expect(store.state).toBe('stale')
    store.dispose()
  })

  it('通道 3a：visibilitychange 回到可见 → 触发检查（受 5 分钟硬节流）', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    // 距上次检查 < 5 分钟 ⇒ 硬节流拦住
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5 * 60 * 1000 + 1000)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(2)
    store.dispose()
  })

  it('通道 3b：30 分钟轮询 → 触发检查', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.getSystemVersion).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(APP_UPDATE_POLL_INTERVAL_MS)
    expect(mocks.getSystemVersion.mock.calls.length).toBeGreaterThanOrEqual(2)
    store.dispose()
  })

  it('通道 4：模块加载失败 → 直接 blocked', () => {
    const store = makeStore()
    store.init()
    expect(store.state).toBe('same')
    notifyModuleLoadFailure('error loading dynamically imported module')
    expect(store.state).toBe('blocked')
    store.dispose()
  })

  it('init 之前到达的事件不会丢（拦截器可能早于 App 挂载）', async () => {
    notifyServerBuild('build-server-bbb')
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.state).toBe('stale')
    store.dispose()
  })

  it('isModuleLoadFailure 只认模块加载特征，不误伤普通导航错误', () => {
    expect(isModuleLoadFailure(new Error('Failed to fetch dynamically imported module: /assets/x.js'))).toBe(true)
    expect(isModuleLoadFailure(new Error('Importing a module script failed.'))).toBe(true)
    expect(isModuleLoadFailure(new Error('Unable to preload CSS for /assets/x.css'))).toBe(true)
    expect(isModuleLoadFailure(new Error('navigation guard error'))).toBe(false)
    expect(isModuleLoadFailure(undefined)).toBe(false)
  })
})

describe('appUpdate store · 更新内容（notes）', () => {
  it('notes 非空 → 有入口，可打开弹窗', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.notes).toEqual(['固件上传改为必填 sidecar'])
    expect(store.hasReleaseNotes).toBe(true)
    store.openReleaseNotes()
    expect(store.releaseNotesVisible).toBe(true)
    store.closeReleaseNotes()
    expect(store.releaseNotesVisible).toBe(false)
    store.dispose()
  })

  it('notes 为空数组 / 缺失 → 不渲染入口，openReleaseNotes 无效', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ notes: [] }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.notes).toEqual([])
    expect(store.hasReleaseNotes).toBe(false)
    store.openReleaseNotes()
    expect(store.releaseNotesVisible).toBe(false)
    store.dispose()
  })

  it('打开/关闭弹窗不改变更新状态、不触发重试', async () => {
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    const callsAfterCheck = mocks.getSystemVersion.mock.calls.length
    expect(store.state).toBe('stale')

    store.openReleaseNotes()
    store.closeReleaseNotes()
    expect(store.state).toBe('stale')
    expect(mocks.getSystemVersion.mock.calls.length).toBe(callsAfterCheck)
    store.dispose()
  })
})

describe('appUpdate store · 展示信息', () => {
  it('版本号优先取服务端；缺失时显示「未知」而不是写死常量', async () => {
    mocks.getSystemVersion.mockResolvedValue(versionPayload({ build_id: LOCAL_BUILD, version: '2.9.0' }))
    const store = makeStore()
    store.init()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.displayVersion).toBe('2.9.0')
    store.dispose()
  })

  it('服务端不可达且无 VITE_APP_VERSION → 「未知」', () => {
    const store = makeStore()
    expect(store.displayVersion).toBe('未知')
    expect(store.displayBuildId).toBe(LOCAL_BUILD)
  })
})
