import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useWebSocketStore } from '../websocket'

// Mock logger to avoid noise
vi.mock('@/utils/logger', () => ({
  logger: {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  },
}))

// Mock import.meta.env
vi.stubEnv('VITE_WS_URL', '/api/v1/ws')

// Mock WebSocket
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
  url: string

  constructor(url: string) {
    this.url = url
    this.readyState = MockWebSocket.CONNECTING
    MockWebSocket.instances.push(this)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }

  send(_data: string) {
    // mock send
  }

  // Test helpers
  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  simulateMessage(data: any) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }

  simulateError() {
    this.onerror?.(new Event('error'))
  }

  simulateClose() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }
}

vi.stubGlobal('WebSocket', MockWebSocket)

describe('useWebSocketStore', () => {
  let store: ReturnType<typeof useWebSocketStore>

  beforeEach(() => {
    setActivePinia(createPinia())
    MockWebSocket.instances = []
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllTimers()
    // 每个用例都从确定的基线环境开始：否则某个用例的 vi.unstubAllEnvs()
    // 会让后续用例落回「未显式 stub」状态，URL 的来源变得不可判定。
    vi.stubEnv('VITE_WS_URL', '/api/v1/ws')
    vi.stubEnv('VITE_BASE_PATH', '')
    store = useWebSocketStore()
  })

  // ── Connection state ──────────────────────────────

  it('starts disconnected', () => {
    expect(store.isConnected).toBe(false)
    expect(store.connected).toBe(false)
  })

  it('isAuthenticated returns false when no token', () => {
    localStorage.clear()
    sessionStorage.clear()
    expect(store.isAuthenticated).toBe(false)
  })

  it('isAuthenticated returns true when token in localStorage', () => {
    localStorage.setItem('token', 'test-token')
    expect(store.isAuthenticated).toBe(true)
  })

  it('isAuthenticated returns true when token in sessionStorage', () => {
    sessionStorage.setItem('token', 'test-token')
    expect(store.isAuthenticated).toBe(true)
  })

  it('getToken returns token from localStorage', () => {
    localStorage.setItem('token', 'ls-token')
    expect(store.getToken()).toBe('ls-token')
  })

  it('getToken returns token from sessionStorage as fallback', () => {
    sessionStorage.setItem('token', 'ss-token')
    expect(store.getToken()).toBe('ss-token')
  })

  it('getToken returns null when no token', () => {
    expect(store.getToken()).toBeNull()
  })

  // ── Connect ───────────────────────────────────────

  it('connect does nothing without token', () => {
    localStorage.clear()
    sessionStorage.clear()
    store.connect()
    expect(MockWebSocket.instances.length).toBe(0)
  })

  it('connect creates WebSocket when token exists', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    expect(MockWebSocket.instances.length).toBe(1)
    // 逐字符全等（原为 toContain('token=test-token')，任何多余路径都会漏过）
    expect(MockWebSocket.instances[0].url).toBe(`ws://${window.location.host}/api/v1/ws?token=test-token`)
  })

  it('connect does not create duplicate when already connected', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    store.connect() // should not create new
    expect(MockWebSocket.instances.length).toBe(1)
  })

  // ── 子路径前缀部署（VITE_BASE_PATH）──────────────────

  it('connect 相对 WS 路径自动带 VITE_BASE_PATH 前缀', () => {
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    localStorage.setItem('token', 'test-token')
    store.connect()
    expect(MockWebSocket.instances.length).toBe(1)
    const url = MockWebSocket.instances[0].url
    // 完整 URL = ws://host + 前缀 + 相对路径 + token
    expect(url).toBe(`ws://${window.location.host}/ehome-dev/api/v1/ws?token=test-token`)
    vi.unstubAllEnvs()
  })

  it('connect 无 VITE_BASE_PATH 时 WS 路径不带前缀', () => {
    vi.stubEnv('VITE_BASE_PATH', '')
    localStorage.setItem('token', 'test-token')
    store.connect()
    expect(MockWebSocket.instances.length).toBe(1)
    // 逐字符全等（原为 toContain('/api/v1/ws')，缺陷 URL 同样通过）
    expect(MockWebSocket.instances[0].url).toBe(`ws://${window.location.host}/api/v1/ws?token=test-token`)
    vi.unstubAllEnvs()
  })

  it('connect VITE_WS_URL 为完整端点时不加前缀、不追加路径', () => {
    vi.stubEnv('VITE_WS_URL', 'wss://example.com/ws')
    vi.stubEnv('VITE_BASE_PATH', '/ehome-dev/')
    localStorage.setItem('token', 'test-token')
    store.connect()
    expect(MockWebSocket.instances.length).toBe(1)
    // 逐字符全等（原为 toContain('wss://example.com/ws')，曾被二次追加为
    // wss://example.com/ws/api/v1/ws 而断言恒真）——规范 §6 P0 的直接回归位。
    expect(MockWebSocket.instances[0].url).toBe('wss://example.com/ws?token=test-token')
    expect(MockWebSocket.instances[0].url).not.toContain('/ehome-dev')
    expect(MockWebSocket.instances[0].url).not.toContain('/api/v1/ws')
    vi.unstubAllEnvs()
  })

  // ── VITE_WS_URL × VITE_BASE_PATH 输入矩阵（规范 §6 P0 回归）──────
  //
  // 契约（二选一，语义唯一）：
  //   完整端点 — 以 ws:// 或 wss:// 开头：原样使用，不追加任何路径，
  //              且不受 VITE_BASE_PATH 影响；wss://host 就是完整端点本身
  //              （等价 wss://host/），不会自动补 /api/v1/ws。
  //   相对路径 — 以 / 开头：拼 VITE_BASE_PATH 前缀后基于当前页面 origin 组装。
  //
  // 每行都断言 new WebSocket 收到的 URL 与期望串逐字符全等：
  // 任何一次多余的路径追加（缺陷）或漏加前缀都会让对应行立即变红。
  describe('VITE_WS_URL 输入矩阵（new WebSocket URL 逐字符全等）', () => {
    const origin = `ws://${window.location.host}`

    it.each([
      ['相对路径 + 根部署', '/api/v1/ws', '', `${origin}/api/v1/ws?token=t`],
      ['相对路径 + 子路径部署', '/api/v1/ws', '/ehome-dev/', `${origin}/ehome-dev/api/v1/ws?token=t`],
      ['完整端点（含路径）+ 根部署', 'wss://host/api/v1/ws', '', 'wss://host/api/v1/ws?token=t'],
      ['完整端点（无路径）+ 根部署', 'wss://host', '', 'wss://host?token=t'],
      ['完整端点 + 子路径前缀（不受影响）', 'wss://example.com/ws', '/ehome-dev/', 'wss://example.com/ws?token=t'],
    ])('%s', (_label, wsUrl, basePath, expected) => {
      vi.stubEnv('VITE_WS_URL', wsUrl)
      vi.stubEnv('VITE_BASE_PATH', basePath)
      localStorage.setItem('token', 't')
      store.connect()
      expect(MockWebSocket.instances.length).toBe(1)
      expect(MockWebSocket.instances[0].url).toBe(expected)
      vi.unstubAllEnvs()
    })
  })

  it('connected becomes true on open', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    expect(store.connected).toBe(true)
    expect(store.isConnected).toBe(true)
  })

  it('connected becomes false on close', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    expect(store.connected).toBe(true)
    ws.simulateClose()
    expect(store.connected).toBe(false)
  })

  // ── Subscribe / handleMessage ─────────────────────

  it('subscribe receives messages of matching type', () => {
    const handler = vi.fn()
    const unsub = store.subscribe('data_update', handler)

    // Manually trigger message handling via connect+simulateMessage
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    ws.simulateMessage({ type: 'data_update', payload: { device_id: 1 } })

    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(expect.objectContaining({
      type: 'data_update',
      payload: expect.objectContaining({ device_id: 1 }),
    }))
    unsub()
  })

  it('subscribe does not receive messages of different type', () => {
    const handler = vi.fn()
    store.subscribe('data_update', handler)

    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    ws.simulateMessage({ type: 'status_report', payload: {} })

    expect(handler).not.toHaveBeenCalled()
  })

  it('subscribe wildcard "*" receives all messages', () => {
    const handler = vi.fn()
    store.subscribe('*', handler)

    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    ws.simulateMessage({ type: 'data_update', payload: {} })
    ws.simulateMessage({ type: 'node_status', payload: {} })

    expect(handler).toHaveBeenCalledTimes(2)
  })

  it('unsubscribe stops receiving messages', () => {
    const handler = vi.fn()
    const unsub = store.subscribe('data_update', handler)

    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    ws.simulateMessage({ type: 'data_update', payload: {} })
    expect(handler).toHaveBeenCalledTimes(1)

    unsub()
    ws.simulateMessage({ type: 'data_update', payload: {} })
    expect(handler).toHaveBeenCalledTimes(1) // still 1, not 2
  })

  // ── onConnected ───────────────────────────────────

  it('onConnected handler fires immediately if already connected', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    MockWebSocket.instances[0].simulateOpen()

    const handler = vi.fn()
    store.onConnected(handler)
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('onConnected handler does not fire when not connected', () => {
    const handler = vi.fn()
    store.onConnected(handler)
    expect(handler).not.toHaveBeenCalled()
  })

  // ── Disconnect ────────────────────────────────────

  it('disconnect closes WebSocket and sets connected=false', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    expect(store.connected).toBe(true)

    store.disconnect()
    expect(store.connected).toBe(false)
  })

  // ── Send ───────────────────────────────────────────

  it('send does nothing when not connected', () => {
    // No connect called → ws is null → send is no-op
    expect(() => store.send({ type: 'ping' })).not.toThrow()
  })
})
