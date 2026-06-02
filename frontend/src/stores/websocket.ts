import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { logger } from '@/utils/logger'

export interface WebSocketMessage {
  type: string
  topic?: string
  payload?: {
    collector_id?: number
    device_id?: number
    status?: string
    devices?: any[]
    data?: any
    progress?: number
    record_id?: number
    latency_ms?: number
  }
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

  const isConnected = computed(() => connected.value)

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

    manuallyClosed.value = false

    const wsUrl = import.meta.env.VITE_WS_BASE_URL || 'ws://localhost:8080'
    let statusUrl = `${wsUrl}/api/v1/ws`

    const token = localStorage.getItem('token')
    if (token) {
      statusUrl = `${statusUrl}?token=${encodeURIComponent(token)}`
    }

    logger.debug('WebSocket 连接中', { url: statusUrl.replace(/token=[^&]+/, 'token=***') })
    ws.value = new WebSocket(statusUrl)

    ws.value.onopen = () => {
      connected.value = true
      logger.info('WebSocket 已连接', { url: statusUrl.replace(/token=[^&]+/, 'token=***') })
      if (reconnectTimer.value) {
        clearTimeout(reconnectTimer.value)
        reconnectTimer.value = null
      }
      startHeartbeat()
      // 通知所有连接处理器
      connectionHandlers.value.forEach(handler => handler())
    }

    ws.value.onmessage = (event: MessageEvent) => {
      try {
        const message: WebSocketMessage = JSON.parse(event.data)
        if (message.type === 'channel_data') {
          console.log('[WS] Received:', message.type, JSON.stringify(message.payload))
        }
        handleMessage(message)
        resetHeartbeat()
      } catch (error) {
        logger.error('WebSocket 消息解析错误', { error: String(error) })
      }
    }

    ws.value.onerror = (event) => {
      logger.error('WebSocket 错误', { event: String(event) })
      connected.value = false
    }

    ws.value.onclose = () => {
      connected.value = false
      logger.warn('WebSocket 已断开')

      if (manuallyClosed.value) {
        return
      }

      // 自动重连
      if (reconnectTimer.value === null) {
        reconnectTimer.value = setTimeout(() => {
          reconnectTimer.value = null
          if (!manuallyClosed.value) {
            connect()
          }
        }, 5000)
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
    stopHeartbeat()
  }

  // 发送消息
  const send = (data: any) => {
    if (ws.value && ws.value.readyState === WebSocket.OPEN) {
      const message = typeof data === 'string' ? data : JSON.stringify(data)
      ws.value.send(message)
    }
  }

  // 心跳
  const startHeartbeat = () => {
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
    }
    heartbeatTimer.value = setInterval(() => {
      if (ws.value && ws.value.readyState === WebSocket.OPEN) {
        ws.value.send(JSON.stringify({ type: 'ping' }))
      }
    }, 30000)
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
    subscribe,
    onConnected,
    connect,
    disconnect,
    send
  }
})
