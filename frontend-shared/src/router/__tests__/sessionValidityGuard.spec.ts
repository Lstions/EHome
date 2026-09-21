import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useUserStore } from '@/stores/user'
import { useWebSocketStore } from '@/stores/websocket'
import type { Router } from 'vue-router'

/**
 * 导航守卫的会话有效性判定（B2）—— 真的走**生产路由**。
 *
 * 缺陷形态（用户被弹来弹去 / 被死锁）：
 *   守卫原来判的是 `userStore.isLoggedIn`，而它只是「token 字符串存在」。
 *   浏览器里残留清库前的旧 JWT 时它恒为 true，于是
 *     /dashboard → WS 握手 401 / HTTP 401 → 踢回 /login
 *     → 守卫认为「已登录」→ 又跳 /dashboard → 再 401 …
 *   在 WS 永久 401（B1 未修）时这条链是**活锁**：用户既进不去也登不了。
 *
 * ⚠️ 为什么不用 guards.spec.ts 里那套「复制一份守卫逻辑」的测试路由：
 * 复制出来的守卫与 src/router/index.ts 会各自演化 —— 改生产守卫而测试仍绿，
 * 正是本仓反复出现的假绿形态。这里直接 import 生产路由单例。
 *
 * ⚠️ 为什么每次断言前要先落到 /403 再出发：vue-router 对「导航到当前所在路由」
 * 会以 duplicated navigation 中止且**不跑守卫**，直接连续 push 同一个目标会让
 * 断言因为「没发生导航」而意外通过/失败（本文件第一版就被咬过）。
 * /403 不要求鉴权，是唯一在所有会话状态下都可达的中性落点。
 */

vi.mock('@/stores/routeProgress', () => ({
  useRouteProgress: () => ({ start: vi.fn(), done: vi.fn(), fail: vi.fn() }),
}))

let router: Router

/** 导航并吞掉重定向/重复导航的 abort；返回最终落点 */
async function go(path: string) {
  await router.push(path).catch(() => {})
  await router.isReady()
  return router.currentRoute.value
}

/** 落到中性路由，保证下一次 push 一定是**真实导航**（守卫真的会跑） */
const neutral = () => go('/403')

describe('路由守卫：会话有效性（B2，生产路由）', () => {
  beforeEach(async () => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    const mod = await import('@/router')
    router = mod.default
    await neutral()
  })

  it('无 token 时 /dashboard 落到 /login（既有行为不回归）', async () => {
    const route = await go('/dashboard')
    expect(route.path).toBe('/login')
    expect(route.query.redirect).toBe('/dashboard')
  })

  it('正常 token（未被判定失效）时 /login 弹回 /dashboard（既有行为不回归）', async () => {
    // 全新 pinia = 模拟「带着残留 token 的一次全新页面加载」：
    // user store 的初始 state 在创建时从 storage 读一次，复用旧实例读不到新值。
    localStorage.setItem('token', 'good-token')
    setActivePinia(createPinia())
    const userStore = useUserStore()
    expect(userStore.isLoggedIn).toBe(true)

    const route = await go('/login')
    expect(route.path).toBe('/dashboard')
  })

  it('残留旧 token 且已被权威判定失效 ⇒ /dashboard 落到 /login', async () => {
    localStorage.setItem('token', 'stale-token')
    setActivePinia(createPinia())
    const userStore = useUserStore()
    const wsStore = useWebSocketStore()
    wsStore.invalidateSession()

    expect(userStore.isLoggedIn).toBe(true) // 关键分母：user store 仍认为「已登录」
    const route = await go('/dashboard')
    expect(route.path).toBe('/login')
  })

  it('失效态下 /login 不再被弹回 /dashboard（这是活锁的关键一环）', async () => {
    localStorage.setItem('token', 'stale-token')
    setActivePinia(createPinia())
    const userStore = useUserStore()
    const wsStore = useWebSocketStore()
    wsStore.invalidateSession()

    // 服务端拒绝后 storage 里可能又被写回同一个死 token（多标签页/旧代码路径）；
    // 此时「token 存在」仍然成立，唯一能拦住弹回的判据就是会话有效性。
    localStorage.setItem('token', 'stale-token')
    expect(userStore.isLoggedIn).toBe(true)

    expect((await go('/dashboard')).path).toBe('/login')

    // 从 /403 出发是**真实导航**：守卫必须真的放过 /login
    await neutral()
    expect((await go('/login')).path).toBe('/login')
  })

  it('重新登录（换新 token）后 /login 恢复弹回 /dashboard', async () => {
    localStorage.setItem('token', 'stale-token')
    setActivePinia(createPinia())
    const wsStore = useWebSocketStore()
    wsStore.invalidateSession()
    await neutral()
    expect((await go('/login')).path).toBe('/login')

    // 登录成功：新 token 落到 storage（user store 随之恢复已登录）
    localStorage.setItem('token', 'fresh-token')
    const userStore = useUserStore()
    userStore.token = 'fresh-token'
    userStore.isLoggedIn = true

    expect(wsStore.isCurrentTokenInvalidated()).toBe(false)
    await neutral()
    expect((await go('/login')).path).toBe('/dashboard')
  })
})
