import { ref, onUnmounted } from 'vue'
import { logger } from '@/utils/logger'

export interface WebSocketMessage {
  type: string
  data?: any
  device_id?: number
  collector_id?: number
  status?: string
  progress?: number
}

export interface WebSocketOptions {
  heartbeat?: boolean // 是否启用心跳
  reconnect?: boolean // 是否自动重连
  reconnectInterval?: number // 重连间隔（毫秒）
  onMessage: (message: WebSocketMessage) => void
  onConnected?: () => void
  onDisconnected?: () => void
  onError?: () => void
}

export function useWebSocket(url: string, options: WebSocketOptions) {
  const {
    heartbeat = true,
    reconnect = true,
    reconnectInterval = 5000,
    onMessage,
    onConnected,
    onDisconnected,
    onError
  } = options

  const ws = ref<WebSocket | null>(null)
  const connected = ref(false)
  const reconnectTimer = ref<any>(null)
  const heartbeatTimer = ref<any>(null)
  const messageQueue = ref<any[]>([])
  const manuallyClosed = ref(false)

  // 连接 WebSocket
  const connect = () => {
    if (ws.value && (ws.value.readyState === WebSocket.OPEN || ws.value.readyState === WebSocket.CONNECTING)) {
      return
    }

    manuallyClosed.value = false
    
    // 获取token并添加到URL
    let wsUrl = url
    const token = localStorage.getItem('token')
    if (token) {
      const separator = url.includes('?') ? '&' : '?'
      wsUrl = `${url}${separator}token=${encodeURIComponent(token)}`
    }
    
    logger.debug('WebSocket连接中', { url: wsUrl.replace(/token=[^&]+/, 'token=***') })
    ws.value = new WebSocket(wsUrl)

    ws.value.onopen = () => {
      connected.value = true
      logger.info('WebSocket已连接')
      // 清除重连定时器
      if (reconnectTimer.value) {
        clearTimeout(reconnectTimer.value)
        reconnectTimer.value = null
      }
      // 启动心跳
      if (heartbeat) {
        startHeartbeat()
      }
      // 发送队列中的消息
      flushMessageQueue()
      // 触发连接回调
      onConnected?.()
    }

    ws.value.onmessage = (event: MessageEvent) => {
      try {
        const message: WebSocketMessage = JSON.parse(event.data)
        // 处理接收到的消息
        onMessage?.(message)
        // 收到消息后重置心跳定时器
        if (heartbeat) {
          resetHeartbeat()
        }
      } catch (error) {
        logger.error('WebSocket消息解析错误', { error: String(error) })
      }
    }

    ws.value.onerror = (event) => {
      logger.error('WebSocket错误', { event: String(event) })
      connected.value = false
      onError?.()
    }

    ws.value.onclose = () => {
      connected.value = false
      onDisconnected?.()

      // 如果是手动关闭或非正常关闭，不重连
      if (manuallyClosed.value) {
        return
      }

      // 如果启用了重连，则延迟重连
      if (reconnect && reconnectInterval > 0) {
        reconnectTimer.value = setTimeout(() => {
          if (!manuallyClosed.value) {
            connect()
          }
        }, reconnectInterval)
      }

      // 清除心跳定时器
      stopHeartbeat()
    }
  }

  // 断开连接
  const disconnect = () => {
    manuallyClosed.value = true
    if (ws.value) {
      ws.value.close()
    }
    // 清除定时器
    if (reconnectTimer.value) {
      clearTimeout(reconnectTimer.value)
      reconnectTimer.value = null
    }
    stopHeartbeat()
  }

  // 发送消息
  const send = (data: any) => {
    const message = typeof data === 'string' ? data : JSON.stringify(data)

    if (ws.value && ws.value.readyState === WebSocket.OPEN) {
      ws.value.send(message)
    } else {
      // 如果未连接，将消息加入队列
      messageQueue.value.push(data)
    }
  }

  // 刷新消息队列
  const flushMessageQueue = () => {
    while (messageQueue.value.length > 0 && ws.value && ws.value.readyState === WebSocket.OPEN) {
      const message = messageQueue.value.shift()
      const messageStr = typeof message === 'string' ? message : JSON.stringify(message)
      ws.value.send(messageStr)
    }
  }

  // 心跳相关
  const startHeartbeat = () => {
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
    }

    heartbeatTimer.value = setInterval(() => {
      if (ws.value && ws.value.readyState === WebSocket.OPEN) {
        ws.value.send(JSON.stringify({ type: 'ping' }))
      }
    }, 30000) // 30秒发一次心跳
  }

  const stopHeartbeat = () => {
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
      heartbeatTimer.value = null
    }
  }

  const resetHeartbeat = () => {
    stopHeartbeat()
    if (heartbeat) {
      startHeartbeat()
    }
  }

  // 组件卸载时清理
  onUnmounted(() => {
    disconnect()
    if (reconnectTimer.value) {
      clearTimeout(reconnectTimer.value)
    }
    if (heartbeatTimer.value) {
      clearInterval(heartbeatTimer.value)
    }
  })

  return {
    connected,
    connect,
    disconnect,
    send,
    // 暴露内部方法供高级使用
    _ws: ws,
    _flushMessageQueue: flushMessageQueue,
    _startHeartbeat: startHeartbeat,
    _stopHeartbeat: stopHeartbeat
  }
}
