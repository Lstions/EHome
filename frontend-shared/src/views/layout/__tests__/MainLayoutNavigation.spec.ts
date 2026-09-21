import { describe, expect, it } from 'vitest'
import layoutSource from '../MainLayout.vue?raw'

describe('MainLayout route navigation UX', () => {
  it('does not remove the current route before the next route is ready', () => {
    expect(layoutSource).not.toContain('mode="out-in"')
  })

  it('preloads the primary list routes after the authenticated layout mounts', () => {
    expect(layoutSource).toContain('preloadPrimaryRoutes')
  })

  it('warms the primary list data caches before the first menu click', () => {
    expect(layoutSource).toContain('warmPrimaryListData')
    expect(layoutSource).toContain('Promise.allSettled')
  })

  it('uses the unified user logout path for session cache clearing', () => {
    expect(layoutSource).toContain('await userStore.logout()')
  })
})

/**
 * WS 状态徽标的三态（B3）。
 *
 * 缺陷形态：徽标只有「在线 / 离线」两态。认证失效与网络中断在界面上完全同形，
 * 但两者对用户的含义相反 —— 前者只能重新登录，后者等网络即可。用户看到的是
 * 一个永远不变的红点，既不知道该做什么，也没有任何入口可做。
 *
 * ⚠️ 为什么用 ?raw 源码断言而不是挂载断言：徽标只在「认证失效**同时**未连接」
 * 这一组合下才呈现第三态，挂载级用例要靠 mock store 的响应式开关才能覆盖，
 * 容易退化成「mock 什么就断言什么」的同义反复。这里钉住的是模板与样式里
 * **真实存在**的三态分支 + 可区分文案 + 恢复入口。
 * 端到端效果由 Playwright 真实浏览器用例与 websocketSessionRecovery.spec.ts 共同覆盖。
 */
describe('MainLayout WS 徽标三态（B3）', () => {
  it('模板绑定的是「当前 token 是否失效」而不是裸布尔（重新登录后必须自动回正常态）', () => {
    expect(layoutSource).toContain('wsStore.isCurrentTokenInvalidated()')
  })

  it('三态文案齐全：在线 / 离线 / 登录已失效', () => {
    expect(layoutSource).toContain("'在线'")
    expect(layoutSource).toContain("'离线'")
    expect(layoutSource).toContain("'登录已失效'")
  })

  it('失效态 title 明确给出「请重新登录」的指引（用户能区分网络断与登录失效）', () => {
    expect(layoutSource).toContain('登录已失效，请重新登录')
    // 离线态也要说清是「网络中断或服务端不可达」，而不是与失效共用一句
    expect(layoutSource).toContain('网络中断')
  })

  it('失效态提供回登录页的入口（否则用户无法自愈）', () => {
    expect(layoutSource).toContain('handleRelogin')
    expect(layoutSource).toContain("router.push('/login')")
    expect(layoutSource).toMatch(/v-if="wsSessionInvalid"/)
    expect(layoutSource).toContain('重新登录')
  })

  it('第三态有独立样式（写了 class 但无样式等于视觉上没做）', () => {
    expect(layoutSource).toMatch(/\.ws-status\.invalid\s*\{/)
  })

  it('新加入的 el-link 用字符串 underline（不得引入布尔形态的弃用告警）', () => {
    expect(layoutSource).toContain('underline="never"')
    expect(layoutSource).not.toMatch(/:underline\s*=\s*"/)
  })
})
