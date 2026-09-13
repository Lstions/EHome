import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { logger } from '@/utils/logger'

export interface WebSocketMessage {
  type: string
  topic?: string
  payload?: {
    edge_device_id?: number
    edge_device_name?: string
    node_id?: number | string
    node_name?: string
    status?: string
    devices?: any[]
    data?: any
    sensors?: any
    collected_at?: string
    raw_hex?: string
    raw_data?: any
    progress?: number
    record_id?: number
    latency_ms?: number
    channel_id?: number
    uptime_seconds?: number
    channel_count?: number
    model?: string
    firmware?: string
    reason?: string
  }
  // Flat-format fields
  data?: any
  edge_device_id?: number
  timestamp?: number
}

type MessageHandler = (message: WebSocketMessage) => void
type ConnectionHandler = () => void

export const useWebSocketStore = defineStore('websocket', () => {
  const ws = ref<WebSocket | null>(null)
  const connected = ref(false)
  const reconnectTimer = ref<ReturnType<typeof setTimeout> | null>(null)
  const heartbeatTimer = ref<ReturnType<typeof setInterval> | null>(null)
  const manuallyClosed = ref(false)
  const messageHandlers = ref<Map<string, Set<MessageHandler>>>(new Map())
  const connectionHandlers = ref<Set<ConnectionHandler>>(new Set())
  // 指数退避重连：5s → 10s → 20s → 40s → 60s（上限60s）
  const reconnectAttempts = ref(0)
  const MAX_RECONNECT_DELAY = 60000
  const BASE_RECONNECT_DELAY = 5000

  const isConnected = computed(() => connected.value)

  // 统一的认证状态检查
  const isAuthenticated = computed(() => {
    try {
      return !!(localStorage.getItem('token') || sessionStorage.getItem('token'))
    } catch {
      return false
    }
  })

  // 获取 token
  const getToken = () => {
    try {
      return localStorage.getItem('token') || sessionStorage.getItem('token')
    } catch {
      return null
    }
  }

  // 订阅消息
  const subscribe = (type: string, handler: MessageHandler) => {
    if (!messageHandlers.value.has(type)) {
      messageHandlers.value.set(type, new Set())
    }
    messageHandlers.value.get(type)!.add(handler)

    // 返回取消订阅函数
    return () => {
      messageHandlers.value.get(type)?.delete(handler)
    }
  }

  // 订阅连接状态变化
  const onConnected = (handler: ConnectionHandler) => {
    connectionHandlers.value.add(handler)
    // 如果已经连接，立即执行
    if (connected.value) {
      handler()
    }
    // 返回取消订阅函数
    return () => {
      connectionHandlers.value.delete(handler)
    }
  }

  // 处理接收到的消息
  const handleMessage = (message: WebSocketMessage) => {
    const handlers = messageHandlers.value.get(message.type)
    if (handlers) {
      handlers.forEach(handler => handler(message))
    }
    // 也通知订阅所有类型的处理器
    const allHandlers = messageHandlers.value.get('*')
    if (allHandlers) {
      allHandlers.forEach(handler => handler(message))
    }
  }

  // 连接 WebSocket
  const connect = () => {
    if (ws.value && (ws.value.readyState === WebSocket.OPEN || ws.value.readyState === WebSocket.CONNECTING)) {
      return
    }

    // Skip if not authenticated
    const token = getToken()
    if (!token) {
      logger.debug('WebSocket skipped: no token')
      return
    }

    manuallyClosed.value = false

    // 反代子路径部署（VITE_BASE_PATH）时，相对 WS 路径自动带前缀。
    // 仅对相对路径（/ 开头）加前缀；完整 URL（ws:// 等）保持原样，由用户显式指定。
    const wsBasePrefix = import.meta.env.VITE_BASE_PATH
      ? import.meta.env.VITE_BASE_PATH.replace(/\/+$/, '')
      : ''
    const rawWsUrl = import.meta.env.VITE_WS_URL || '/api/v1/ws'
    const wsUrl = rawWsUrl.startsWith('/')
      ? (wsBasePrefix ? `${wsBasePrefix}${rawWsUrl}` : rawWsUrl)
      : rawWsUrl
    let statusUrl: string

    // VITE_WS_URL 语义契约（唯一，二选一；收敛规范 §6 P0）：
    //   (a) 完整端点：以 ws:// 或 wss:// 开头 → 原样使用，绝不追加任何路径。
    //       注意 wss://host（无路径）同样按完整端点处理，结果为 wss://host?token=...，
    //       不会补默认路径 —— 需要默认路径时请显式配置 wss://host/api/v1/ws。
    //   (b) 相对路径：以 / 开头 → 拼 VITE_BASE_PATH 前缀后基于当前页面 origin 组装。
    // 两种语义互斥：完整端点不受 VITE_BASE_PATH 影响，相对路径才受其影响。
    if (wsUrl.startsWith('ws://') || wsUrl.startsWith('wss://')) {
      statusUrl = wsUrl
    } else {
      // Relative path — construct from current page origin
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      statusUrl = `${protocol}//${window.location.host}${wsUrl}`
    }

    statusUrl = `${statusUrl}?token=${encodeURIComponent(token)}`

    logger.debug('WebSocket 连接中', { url: statusUrl.replace(/token=[^&]+/, 'token=***') })
    ws.value = new WebSocket(statusUrl)

    // Capture the current WebSocket instance in a local variable.
    // This prevents closures (onclose/onerror/etc.) from accessing a different
    // instance if connect() is called again (e.g. reconnect) before the old
    // socket's events fire.
    const sock = ws.value

    sock.onopen = () => {
      connected.value = true
      reconnectAttempts.value = 0  // 连接成功后重置退避计数
      logger.info('WebSocket 已连接')
      if (reconnectTimer.value) {
        clearTimeout(reconnectTimer.value)
        reconnectTimer.value = null
      }
      startHeartbeat()
      // 通知所有连接处理器
      connectionHandlers.value.forEach(handler => handler())
    }

    sock.onmessage = (event: MessageEvent) => {
      try {
        const message: WebSocketMessage = JSON.parse(event.data)
        handleMessage(message)
        resetHeartbeat()
      } catch (error) {
        logger.error('WebSocket 消息解析错误', { error: String(error) })
      }
    }

    sock.onerror = (event) => {
      logger.error('WebSocket 错误', { event: String(event) })
      connected.value = false
    }

    sock.onclose = () => {
      connected.value = false
      logger.warn('WebSocket 已断开')

      if (manuallyClosed.value) {
        return
      }

      // 指数退避重连
      if (reconnectTimer.value === null) {
        const delay = Math.min(
          BASE_RECONNECT_DELAY * Math.pow(2, reconnectAttempts.value),
          MAX_RECONNECT_DELAY
        )
        reconnectAttempts.value++
        logger.info(`WebSocket 将在 ${delay / 1000}s 后重连（第 ${reconnectAttempts.value} 次）`)
        reconnectTimer.value = setTimeout(() => {
          reconnectTimer.value = null
          if (!manuallyClosed.value) {
            connect()
          }
        }, delay)
      }

      stopHeartbeat()
    }
  }

  // 断开连接
  const disconnect = () => {
    manuallyClosed.value = true
    if (ws.value) {
      ws.value.close()
    }
    if (reconnectTimer.value) {
      clearTimeout(reconnectTimer.value)
      reconnectTimer.value = null
    }
    reconnectAttempts.value = 0
    stopHeartbeat()
  }

  // 发送消息
  const send = (data: any) => {
    if (ws.value && ws.value.readyState === WebSocket.OPEN) {
      const message = typeof data === 'string' ? data : JSON.stringify(data)
      ws.value.send(message)
    }
  }

  // 心跳 — 45s间隔，收到任何消息也重置（服务端活跃时不需要额外ping）
  const startHeartbeat = () => {
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
    }
    heartbeatTimer.value = setInterval(() => {
      if (ws.value && ws.value.readyState === WebSocket.OPEN) {
        ws.value.send(JSON.stringify({ type: 'ping' }))
      }
    }, 45000)
  }

  const stopHeartbeat = () => {
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
      heartbeatTimer.value = null
    }
  }

  const resetHeartbeat = () => {
    stopHeartbeat()
    startHeartbeat()
  }

  return {
    connected,
    isConnected,
    isAuthenticated,
    getToken,
    subscribe,
    onConnected,
    connect,
    disconnect,
    send
  }
})
