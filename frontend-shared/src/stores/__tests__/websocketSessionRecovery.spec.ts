import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWebSocketStore } from '../websocket'

/**
 * WS 握手 401 的会话自愈门禁（B1 / B2）。
 *
 * 缺陷形态（用户可见症状：登录后徽标一直是「离线」，怎么刷新都出不来）：
 *   浏览器里残留清库前的旧 JWT → API 走 axios 拦截器会被 401 清掉，
 *   但 **WS 握手 401 不是 axios 请求**，走不到 client.ts 的那段逻辑
 *   ⇒ token 永驻 storage、apiClient 一直带着这个死 token、
 *     徽标死锁在「离线」，用户没有任何自愈路径。
 *
 * 修复后的判定链（本文件逐条钉死）：
 *   连接关闭 → ① 关闭时的 token 是否仍是本次连接用的那个？（who-am-I）
 *              ② 另发一次带凭证的 HTTP 探测，只有 401/403 才算「已被服务端拒绝」
 *   两问都过 → 与 client.ts 同一条清理路径清 token + 置失效标记 + 停止重连。
 *
 * ⚠️ 红线（本文件里最关键的一条断言）：网络抖动 / 探测超时 / 5xx
 * **绝不能**被当成认证失效 —— 那会把正在正常使用的用户直接踢下线。
 */

vi.mock('@/utils/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

class MockWebSocket {
  static instances: MockWebSocket[] = []
  static OPEN = 1
  static CONNECTING = 0
  static CLOSING = 2
  static CLOSED = 3

  readyState = MockWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: ((event: any) => void) | null = null
  onclose: (() => void) | null = null
  closedByClient = false
  url: string

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  close() {
    this.closedByClient = true
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }

  send(_data: string) {}

  simulateClose() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }
}

vi.stubGlobal('WebSocket', MockWebSocket)

/** 探测响应的可控桩；默认「网络不可达」，每条用例按需覆盖 */
const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

const jsonResponse = (status: number, body: unknown = { code: status, message: '', data: null }) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })

const flush = async () => {
  // 探测是 fetch().then() 链，跑完需要几轮微任务
  for (let i = 0; i < 6; i++) await Promise.resolve()
}

describe('WS 会话自愈（B1/B2）', () => {
  let store: ReturnType<typeof useWebSocketStore>

  beforeEach(() => {
    setActivePinia(createPinia())
    MockWebSocket.instances = []
    localStorage.clear()
    sessionStorage.clear()
    fetchMock.mockReset()
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch')) // 默认：网络不可达
    vi.stubEnv('VITE_WS_URL', '/api/v1/ws')
    vi.stubEnv('VITE_BASE_PATH', '')
    store = useWebSocketStore()
  })

  afterEach(() => {
    vi.unstubAllEnvs()
  })

  const connectWith = (token: string, remember = false) => {
    ;(remember ? localStorage : sessionStorage).setItem('token', token)
    store.connect()
    return MockWebSocket.instances[MockWebSocket.instances.length - 1]
  }

  // ── 主路径：401 → 清 token → 会话失效 ──────────────────────

  it('B1：探测到 401 时清除两个 storage 的 token 并置会话失效', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { code: 401, message: 'invalid token' }))
    const sock = connectWith('stale-token')
    // 连接关闭前 storage 里**仍是**本次连接用的那个 token（who-am-I 通过）
    sock.simulateClose()
    await flush()

    expect(localStorage.getItem('token')).toBeNull()
    expect(sessionStorage.getItem('token')).toBeNull()
    expect(store.sessionInvalidated).toBe(true)
    expect(store.invalidatedToken).toBe('stale-token')
    expect(store.isCurrentTokenInvalidated()).toBe(true)
    expect(store.isAuthenticated).toBe(false)
    expect(store.authFailureReason).toBe('session_expired')
  })

  it('B1：403 同样判定为凭证被拒', async () => {
    fetchMock.mockResolvedValue(jsonResponse(403, { code: 403, message: 'forbidden' }))
    const sock = connectWith('stale-token')
    sock.simulateClose()
    await flush()
    expect(store.sessionInvalidated).toBe(true)
    expect(sessionStorage.getItem('token')).toBeNull()
  })

  it('B1：探测用的是带 Bearer 凭证的 /api/v1/account（权威判定，不猜）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))
    const sock = connectWith('probe-me')
    sock.simulateClose()
    await flush()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/account')
    expect((init as RequestInit).headers).toMatchObject({ Authorization: 'Bearer probe-me' })
  })

  it('B1：失效后不再安排重连（清 token ⇒ connect() 自身 early-return，退避必须停）', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockResolvedValue(jsonResponse(401))
      const sock = connectWith('stale-token')
      sock.simulateClose()
      await vi.advanceTimersByTimeAsync(0)
      const instancesAfterClose = MockWebSocket.instances.length

      // 退避 5s→10s→20s→40s→60s 全跑一遍，一条新连接都不该出现
      await vi.advanceTimersByTimeAsync(200000)
      expect(MockWebSocket.instances.length).toBe(instancesAfterClose)
      expect(store.getToken()).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('B1：会话失效后再调 connect() 不再发起连接', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))
    const sock = connectWith('stale-token')
    sock.simulateClose()
    await flush()
    const before = MockWebSocket.instances.length

    store.connect()
    expect(MockWebSocket.instances.length).toBe(before)
    expect(store.isAuthenticated).toBe(false)
  })

  it('B1：失效是幂等的（重复触发只广播一次）', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))
    const sock = connectWith('stale-token')
    sock.simulateClose()
    await flush()
    const nonce = store.authInvalidationNonce

    store.invalidateSession()
    store.invalidateSession()
    expect(store.authInvalidationNonce).toBe(nonce)
  })

  // ── 红线：网络问题绝不能被判成认证失效 ───────────────────

  it('红线：探测网络不可达时**保留** token、不置失效、照常退避重连', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
      const sock = connectWith('good-token')
      sock.simulateClose()
      await vi.advanceTimersByTimeAsync(0)

      expect(store.sessionInvalidated).toBe(false)
      expect(store.isCurrentTokenInvalidated()).toBe(false)
      expect(store.isAuthenticated).toBe(true)
      expect(sessionStorage.getItem('token')).toBe('good-token')

      // 5s 后重连：确实开了一条新连接（网络抖动的正确处置是重试，不是踢人）
      await vi.advanceTimersByTimeAsync(5100)
      expect(MockWebSocket.instances.length).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('红线：服务端 5xx 时保留 token，不判失效', async () => {
    fetchMock.mockResolvedValue(jsonResponse(503, { code: 503, message: 'unavailable' }))
    const sock = connectWith('good-token')
    sock.simulateClose()
    await flush()

    expect(store.sessionInvalidated).toBe(false)
    expect(sessionStorage.getItem('token')).toBe('good-token')
  })

  it('红线：探测抛超时（AbortError）时保留 token', async () => {
    fetchMock.mockRejectedValue(Object.assign(new Error('timeout'), { name: 'TimeoutError' }))
    const sock = connectWith('good-token')
    sock.simulateClose()
    await flush()
    expect(store.sessionInvalidated).toBe(false)
    expect(sessionStorage.getItem('token')).toBe('good-token')
  })

  it('红线：会话有效（200 JSON）时保留 token 并继续重连', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockResolvedValue(jsonResponse(200, { code: 200, message: 'ok', data: { id: 1 } }))
      const sock = connectWith('good-token')
      sock.simulateClose()
      await vi.advanceTimersByTimeAsync(0)
      expect(store.sessionInvalidated).toBe(false)
      expect(sessionStorage.getItem('token')).toBe('good-token')

      await vi.advanceTimersByTimeAsync(5100)
      expect(MockWebSocket.instances.length).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('红线：探测拿到 200 但内容是 HTML（反代把 401 改写成登录页）时**不**判有效、也不判失效', async () => {
    fetchMock.mockResolvedValue(
      new Response('<!doctype html><html>login</html>', { status: 200, headers: { 'content-type': 'text/html' } }),
    )
    const sock = connectWith('maybe-stale')
    sock.simulateClose()
    await flush()
    expect(store.sessionInvalidated).toBe(false)
    expect(sessionStorage.getItem('token')).toBe('maybe-stale')
  })

  // ── who-am-I：别拿旧连接的关闭事件误杀新会话 ───────────────

  it('红线：关闭时 token 已换成新值 ⇒ 不探测、不清 token、不动新会话', async () => {
    const sock = connectWith('old-token')
    // 用户重新登录：storage 换了新 token 之后，旧连接才关闭
    sessionStorage.setItem('token', 'new-token')
    sock.simulateClose()
    await flush()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('token')).toBe('new-token')
    expect(store.sessionInvalidated).toBe(false)
  })

  it('红线：关闭时 token 已被清空（别处已处理）⇒ 不重复动作、不误报失效', async () => {
    const sock = connectWith('old-token')
    sessionStorage.removeItem('token')
    sock.simulateClose()
    await flush()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(store.sessionInvalidated).toBe(false)
  })

  it('已被裁定失效的凭证再次关闭 ⇒ 直接收敛，不再重复探测', async () => {
    // 先让 401 路径完成一次裁定
    fetchMock.mockResolvedValue(jsonResponse(401))
    const first = connectWith('doomed')
    first.simulateClose()
    await flush()
    expect(store.sessionInvalidated).toBe(true)
    fetchMock.mockClear()

    // 模拟 storage 里又被写回同一个已死 token（例如多标签页/别的代码路径）
    sessionStorage.setItem('token', 'doomed')
    store.connect()
    const second = MockWebSocket.instances[MockWebSocket.instances.length - 1]
    second.simulateClose()
    await flush()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('token')).toBeNull()
    expect(store.isCurrentTokenInvalidated()).toBe(true)
  })

  // ── B2：重新登录后必须恢复 ─────────────────────────────

  it('B2：重新登录（storage 换新 token）后，失效结论立刻作废、可重建连接', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401))
    const sock = connectWith('stale-token')
    sock.simulateClose()
    await flush()
    expect(store.isAuthenticated).toBe(false)

    // 登录成功：新 token 落到 storage，MainLayout 用新凭证换连接
    sessionStorage.setItem('token', 'fresh-token')
    store.reconnectWithFreshToken()

    expect(store.isCurrentTokenInvalidated()).toBe(false)
    expect(store.sessionInvalidated).toBe(false)
    expect(store.isAuthenticated).toBe(true)
    expect(MockWebSocket.instances.length).toBe(2)
    expect(MockWebSocket.instances[1].url).toContain('token=fresh-token')
  })

  it('B2：旧 token 仍在 storage 时 isAuthenticated 为 true（未失效前不误伤正常会话）', () => {
    sessionStorage.setItem('token', 'still-good')
    expect(store.isAuthenticated).toBe(true)
    expect(store.isCurrentTokenInvalidated()).toBe(false)
  })

  it('B2：HTTP 拦截器清 token 后，store 的失效标记自愈（否则重新登录会被自己挡住）', () => {
    sessionStorage.setItem('token', 'x')
    store.invalidateSession()
    expect(store.isAuthenticated).toBe(false)

    // 用户重新登录 → 新 token；此时 store 仍持有旧结论
    sessionStorage.setItem('token', 'y')
    store.resetAuthStateAfterStaleToken()

    expect(store.sessionInvalidated).toBe(false)
    expect(store.isAuthenticated).toBe(true)
    expect(store.invalidatedToken).toBeNull()
  })

  it('B2：resetAuthStateAfterStaleToken 在 storage 为空时不得抹掉刚生效的失效结论', () => {
    sessionStorage.setItem('token', 'x')
    store.invalidateSession()
    expect(store.sessionInvalidated).toBe(true)

    store.resetAuthStateAfterStaleToken()
    expect(store.sessionInvalidated).toBe(true)
    expect(store.isCurrentTokenInvalidated()).toBe(true)
  })

  it('B2：同一 token 仍被判失效时，resetAuthStateAfterStaleToken 不作废结论', () => {
    sessionStorage.setItem('token', 'x')
    store.invalidateSession()
    sessionStorage.setItem('token', 'x') // 又写回同一个死 token
    store.resetAuthStateAfterStaleToken()
    expect(store.isCurrentTokenInvalidated()).toBe(true)
    expect(store.isAuthenticated).toBe(false)
  })

  // ── 换连接的既有行为不得回归 ───────────────────────────

  it('用手动退出后的 connect() 不得因早退而偷偷打开重连链', () => {
    sessionStorage.setItem('token', 't')
    store.connect()
    store.disconnect()
    const before = MockWebSocket.instances.length
    store.connect()
    // disconnect() 后 connect() 是显式行为，允许新建连接；这里只确认没有崩/没有重复
    expect(MockWebSocket.instances.length).toBe(before + 1)
  })

  // ── 重连风暴门禁（真实浏览器验证时发现的形态）─────────────
  //
  // 换连接时，新连接在本次 connect() 结束时仍处于 CONNECTING。任何「观察到
  // 凭证/状态变化就再换一次」的消费方（哪怕判据是 connected===false）都会把
  // 刚建好的连接又关掉，如此往复 —— 表现为徽标永远连不上、控制台刷屏重连。
  // 判据：连续多次「凭证变更 + 调用换连接」后，连接数必须线性增长而不是爆炸。

  it('红线：连续换凭证不会引发重连风暴（每个新凭证只建一条连接）', () => {
    sessionStorage.setItem('token', 't0')
    store.connect()
    for (let i = 1; i <= 5; i++) {
      sessionStorage.setItem('token', 't' + i)
      store.reconnectWithFreshToken()
    }
    // 首次 connect + 5 次换连接 = 6 条，绝不允许出现「被自己的事件反复关闭」
    expect(MockWebSocket.instances.length).toBe(6)
    // 且最后一条连接携带的是最后一个凭证（没有退回到旧连接状态）
    expect(MockWebSocket.instances[5].url).toContain('token=t5')
  })

  it('红线：换连接后不得再被自己触发一次（store 无「连接已变更」自反馈事件）', () => {
    sessionStorage.setItem('token', 'a')
    store.connect()
    const before = MockWebSocket.instances.length
    sessionStorage.setItem('token', 'b')
    store.reconnectWithFreshToken()
    expect(MockWebSocket.instances.length).toBe(before + 1)
    // 让所有微任务/定时器机会跑完，仍不得冒出额外连接
    return Promise.resolve().then(() => Promise.resolve()).then(() => {
      expect(MockWebSocket.instances.length).toBe(before + 1)
    })
  })

  it('换连接：旧连接处于 CONNECTING 时被静默放弃，新连接带新凭证', () => {
    sessionStorage.setItem('token', 'old')
    store.connect()
    const first = MockWebSocket.instances[0]
    expect(first.readyState).toBe(MockWebSocket.CONNECTING)

    sessionStorage.setItem('token', 'new')
    store.reconnectWithFreshToken()

    expect(MockWebSocket.instances.length).toBe(2)
    expect(MockWebSocket.instances[1].url).toContain('token=new')
    expect(first.closedByClient).toBe(true)
  })
})
