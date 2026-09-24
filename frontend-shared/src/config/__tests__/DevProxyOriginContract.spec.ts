import { describe, it, expect } from 'vitest'
import viteConfigSource from '../../../vite.config.ts?raw'

/**
 * dev server 代理的 **Origin 契约**（回归围栏）。
 *
 * ## 缺陷形态（2026-09-24 实机复现）
 *
 * `pnpm dev`（5174）访问任何页面，登录后顶部常驻横幅：
 *
 *     与服务器的连接已断开
 *     正在尝试重新连接...
 *
 * 且每 10s 重连一次（`NetworkBanner.vue` 的定时器），实时数据永不更新。
 *
 * ## 根因（已用 4 组判别性测试锁定）
 *
 * 后端 WebSocket 升级有 CSRF 防护：`checkOrigin` 要求 `Origin.host === r.Host`
 * （`backend/internal/websocket/websocket.go:82`）。
 *
 * | Origin | r.Host | 结果 |
 * |---|---|---|
 * | `127.0.0.1:8080` | `127.0.0.1:8080` | 101 ✅ |
 * | `127.0.0.1:5174` | `127.0.0.1:8080` | **403** ❌ |
 *
 * 而 Vite 代理原先写了 `changeOrigin: true` —— 它把 `Host` **改写**成后端地址
 * （8080），浏览器发的 `Origin` 却仍是 5174 ⇒ 不匹配 ⇒ **WS 必然 403**。
 * 后端日志逐条对应：`WebSocket upgrade failed: ... request origin not allowed
 * by Upgrader.CheckOrigin`。
 *
 * ## 为什么在代理侧修而不是改后端
 *
 * 生产部署前端 dist 与后端**同容器同端口**（`EHOME_EXTERNAL_HOST=<host>:8080`），
 * 同源校验在生产完全正确，是安全设计（提交 `6b764ffa` 安全加固）。
 * 只有 dev 的"5174 前端 + 8080 后端"跨源场景需要对齐，故 `changeOrigin: false`
 * 让 Host 原样透传 ⇒ 与 Origin 一致。
 *
 * ## 为什么用源码断言而不是跑起 dev server
 *
 * 单测里起真实代理 + 真实浏览器成本过高；而这条契约的**唯一**失败方式就是
 * 配置文件里那个布尔值被改回 `true`，源码断言足以拦住它。
 * 端到端效果已在实机用 Playwright 验证（握手 101、收到帧、横幅消失）。
 */
describe('vite dev server 代理 Origin 契约（防 WS 403 回归）', () => {
  /**
   * 剥掉注释后再断言。
   *
   * 这一步是必须的：本文件的说明文字里出现了 `changeOrigin:true` 字样
   * （用来解释"为什么不能这么写"），不剥注释会把**注释**当成配置报假红 ——
   * 我第一版就是这么写的，被这条用例自己拦下了（本仓 skill 也记录了同类坑：
   * `?raw` 断言被组件头注释里的历史描述误伤）。
   */
  const codeOnly = viteConfigSource
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')

  it('所有 proxy 条目都必须 changeOrigin: false（改回 true 会让 WS 必然 403）', () => {
    const values = [...codeOnly.matchAll(/changeOrigin:\s*(true|false)/g)].map(m => m[1])
    expect(values.length, '一个 changeOrigin 都没找到 —— 正则失效或配置被重写').toBeGreaterThanOrEqual(2)
    expect(
      values.filter(v => v === 'true'),
      'changeOrigin:true 会改写 Host，使 Origin.host !== r.Host ⇒ WS 被后端 CheckOrigin 拒绝（403）',
    ).toEqual([])
  })

  it('ws: true 必须保留（否则 /api 上的 WS 升级不走代理）', () => {
    const targets = codeOnly.match(/target:\s*(apiTarget|wsTarget)/g) ?? []
    const wsFlags = codeOnly.match(/ws:\s*true/g) ?? []
    expect(wsFlags.length, '缺少 ws:true —— WebSocket 升级不会被代理').toBeGreaterThanOrEqual(2)
    expect(targets.length, '未找到 target 定义').toBeGreaterThanOrEqual(2)
  })

  it('反证：注释里必须保留"为什么不能改成 true"的说明（防止后人好心改错）', () => {
    // 这条容易被视为"没必要的文档断言"，但本缺陷的特征正是
    // "配置看起来更规范（changeOrigin:true 是常见推荐写法）却导致线上式故障"，
    // 没有注释解释时极可能被改回去。
    expect(viteConfigSource).toContain('CheckOrigin')
    expect(viteConfigSource).toMatch(/changeOrigin.*必须为|必须为 \*\*false\*\*/)
  })
})
