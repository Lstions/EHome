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
    expect(MockWebSocket.instances[0].url).toContain('token=test-token')
  })

  it('connect does not create duplicate when already connected', () => {
    localStorage.setItem('token', 'test-token')
    store.connect()
    const ws = MockWebSocket.instances[0]
    ws.simulateOpen()
    store.connect() // should not create new
    expect(MockWebSocket.instances.length).toBe(1)
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
