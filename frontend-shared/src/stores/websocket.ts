import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { logger } from '@/utils/logger'
import { clearSessionCaches } from '@/utils/sessionCache'
import { basePrefix } from '@/utils/basePath'

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

  // ── 会话失效状态（B1/B2/B3）──────────────────────────────────
  // 一个 token 只有两种终结方式：过 HTTP 拦截器（api/client.ts）或过 WS 握手。
  // 后者不是 axios 请求，走不到 client.ts 的 401 分支，所以这里必须**自证**失效
  // 后再走同一条清理路径；否则失效 token 永驻 storage、apiClient 一直带着它、
  // 徽标死锁在「离线」且无法自愈（这正是用户遇到的「登录后一直离线」）。
  const sessionInvalidated = ref(false)
  // 被判失效的凭证**字面值**。会话失效永远只针对某一个具体 token：
  // 用户重新登录后拿到的是另一个 token，旧结论不能再套用到新会话上，
  // 否则重新登录会变成「登录成功却出不去登录页」的活锁（守卫按旧结论继续拦）。
  const invalidatedToken = ref<string | null>(null)
  // storage 的观测版本号：storage 本身不是响应式的，任何依赖它的 computed
  // 在外部清 token 后会返回**缓存的旧值**。改动 token 的地方都 bump 它，
  // 保证 isAuthenticated 这类派生状态重新求值。
  const storageRevision = ref(0)
  // 失效原因：'session_expired' = 服务端已判定该凭证无效。
  // 刻意与网络抖动区分开（后者不改这个值），B3 的徽标文案据此分流。
  const authFailureReason = ref<'session_expired' | null>(null)
  // 每次失效自增：消费方（MainLayout）watch 它发一次性提示，
  // 避免「一个失效事件弹多次」或「每次重渲染都弹」。
  const authInvalidationNonce = ref(0)

  // 统一的认证状态检查。
  // ⚠️ 不能只看「token 字符串是否存在」（B2）：旧 token 也在 storage 里，
  // 只判存在会让 isAuthenticated 在服务端已拒绝该凭证时仍返回 true ⇒
  // 路由守卫把 /login 又弹回 /dashboard，用户被弹来弹去。
  // 判定收敛为：token 存在 **且** 本进程未收到权威失效证据（sessionInvalidated）。
  const isAuthenticated = computed(() => {
    storageRevision.value // 依赖：storage 变化后必须重新求值，否则会返回缓存的旧结论
    const token = getToken()
    if (!token) return false
    if (sessionInvalidated.value && token === invalidatedToken.value) return false
    return true
  })

  /**
   * 当前持久化凭证是否已被权威判定失效。
   *
   * 刻意读 storage 而不是读缓存：导航守卫在**每次导航**都会问这个问题，
   * 而用户可能刚刚重新登录（storage 已换新 token，store 里的失效结论还没清）。
   * 判据收敛为「失效结论 + 同一字面值 token」，换了 token 即自动作废。
   */
  const isCurrentTokenInvalidated = () => {
    if (!sessionInvalidated.value) return false
    const token = getToken()
    if (!token) return true // 已被清掉：仍处失效态（徽标/跳转按失效处理）
    return token === invalidatedToken.value
  }

  // 获取 token
  const getToken = () => {
    try {
      return localStorage.getItem('token') || sessionStorage.getItem('token')
    } catch {
      return null
    }
  }

  // ── 认证失效判定与收敛（B1）─────────────────────────────────

  /** 与 api/client.ts 的 401 分支**同一条**清理路径；两处必须共用，否则状态会漂移 */
  const clearAuthToken = () => {
    clearSessionCaches()
    localStorage.removeItem('token')
    sessionStorage.removeItem('token')
    storageRevision.value++
    // user store 的 isLoggedIn 只在 token 存在时才可能为 true，
    // token 清掉后其下一次读取即为 false —— 这里不 import user store，
    // 避免 websocket ↔ user 之间的循环依赖。
  }

  /** 与 api/client.ts 相同的 API 基址推导（VITE_API_BASE_URL 优先，否则跟随 VITE_BASE_PATH） */
  const apiUrl = (path: string) => {
    const absolute = import.meta.env.VITE_API_BASE_URL
    if (absolute) return `${String(absolute).replace(/\/+$/, '')}${path}`
    return `${basePrefix()}${path}`
  }

  /**
   * 权威判定「当前持久化的 token 是否已不被服务端接受」。
   *
   * 为什么不能靠 WebSocket 自身：浏览器原生 WebSocket 的 error/close 事件
   * **不暴露 HTTP 状态码**（握手 401 与网络不可达在 JS 侧完全同形），
   * 因此必须另发一次带凭证的 HTTP 请求来取得权威结论。
   *
   * 为什么不用 apiClient：其 401 分支会**硬跳转** location.assign('/login')。
   * 由 WS 触发的探测若走那条路，用户会在「正在重连」时被无提示弹回登录页；
   * 且拦截器会把 401 包成 ApiError，状态码要多绕一层。这里用原生 fetch：
   *   · redirect: 'error' —— 不跟随重定向。若认证失败被反代重写成 3xx 登录页，
   *     静默跟随会拿到 200 的 HTML 登录页 ⇒ 误判「会话有效」。宁可不判定。
   *   · AbortSignal.timeout(4000) —— 探测自己不能拖住重连节奏。
   *   · 任何网络层失败（catch / 非 2xx 非 401-403）一律返回 indeterminate，
   *     **绝不**升级为认证失效：网络抖动不能踢掉用户（这是本修复的红线）。
   */
  const probeSessionValidity = async (): Promise<'valid' | 'expired' | 'indeterminate'> => {
    let response: Response
    try {
      response = await fetch(apiUrl('/api/v1/account'), {
        method: 'GET',
        headers: { Authorization: `Bearer ${getToken() ?? ''}`, Accept: 'application/json' },
        // 跟随重定向（默认）**但**要求响应仍是 JSON：
        // 认证失败若被反代改写成 3xx 跳到登录页，跟随会拿到 200 的 HTML
        // 登录页 —— 只看 response.ok 会把它读成「会话有效」。宁可不判定。
        signal: AbortSignal.timeout(4000),
      })
    } catch {
      return 'indeterminate'
    }
    if (response.status === 401 || response.status === 403) return 'expired'
    if (!response.ok) return 'indeterminate'
    const contentType = response.headers.get('content-type') ?? ''
    return contentType.includes('application/json') ? 'valid' : 'indeterminate'
  }

  /**
   * 认证失效的**唯一**收敛点：置标记 + 清 token + 广播。
   * 幂等（重复调用不会重复广播）；清 token 后 getToken() 为空，
   * connect() 会 early-return ⇒ 无意义的重连退避天然停止。
   */
  const invalidateSession = (reason: 'session_expired' = 'session_expired', notify = true) => {
    if (sessionInvalidated.value) return
    // 先记下被拒凭证，再清 storage —— 顺序不能反，否则拿不到字面值
    invalidatedToken.value = getToken()
    sessionInvalidated.value = true
    authFailureReason.value = reason
    clearAuthToken()
    if (notify) {
      authInvalidationNonce.value++
      logger.warn('会话已失效（服务端拒绝该凭证），已清除本地登录态')
    }
  }

  /**
   * HTTP 拦截器（client.ts）清 token 后的对齐：
   * 它清 token 时只改 storage，不知道 store 里还有 sessionInvalidated，
   * 若不归零，用户重新登录后 connect() 反而会被自己的过期标记挡住。
   * 仅当 storage 里确实已有 token 时才归零 —— 刚失效的瞬间 storage 是空的，
   * 归零会把失效标记当场抹掉。
   */
  const resetAuthStateAfterStaleToken = () => {
    const token = getToken()
    if (!token) return
    if (token === invalidatedToken.value) return // 还是那个被拒的凭证，结论依然成立
    sessionInvalidated.value = false
    authFailureReason.value = null
    invalidatedToken.value = null
    storageRevision.value++
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
  const connect = (expectedToken?: string, abandonPrevious = false) => {
    if (ws.value && (ws.value.readyState === WebSocket.OPEN || ws.value.readyState === WebSocket.CONNECTING)) {
      // 凭证已被替换时不能直接把新连接发给服务端 —— 服务端对同一主体只保留
      // 最新一条会话，旧连接随后会被踢，其 onclose 反过来又触发一次换连接，
      // 形成「旧连接反复踢掉新连接」的抖动。这里先主动、静默地关掉旧连接，
      // 再开新的；旧连接的 onclose 会因 ws.value 已不是它而安静退出。
      if (abandonPrevious && ws.value.readyState === WebSocket.CONNECTING) {
        const stale = ws.value
        ws.value = null
        try { stale.close() } catch { /* 已关闭则忽略 */ }
      } else {
        return
      }
    }

    // HTTP 拦截器可能已经清掉 token 而 store 还不知道（见 resetAuthStateAfterStaleToken）。
    // 这里自愈对齐：清空意味着用户已（被）登出，那 token 存在时残留的失效标记必须归零，
    // 否则重新登录后 connect() 会被自己的过期标记挡住。
    resetAuthStateAfterStaleToken()

    // 凭证已被替换：旧连接已退出，重连计数归零，新会话从头开始退避。
    // 刻意**不**在这里向外广播事件：换连接是异步的（新连接此时还在 CONNECTING），
    // 任何「再换一次」的消费方都会把刚建的连接又关掉，退化成无限重连风暴。
    // 需要感知凭证变化的消费方（MainLayout）watch userStore.token 即可。
    if (abandonPrevious) reconnectAttempts.value = 0

    // Skip if not authenticated（含「本进程已判定该凭证失效」）
    if (!isAuthenticated.value) {
      logger.debug('WebSocket skipped: no valid session')
      return
    }

    const token = getToken()
    if (!token) {
      logger.debug('WebSocket skipped: no token')
      return
    }

    // 只有确定要发起的连接才解除「手动关闭」，否则一次被凭证/会话状态挡下的
    // connect() 会顺手把整条重连链打开（disconnect() 之后又自己连回来）。
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
    // 本次连接所携带的凭证快照。onclose 必须与它比较，否则会在
    // 「用户已重新登录拿到新 token」之后，拿旧连接的关闭事件把新会话误杀。
    const attemptToken = expectedToken ?? token
    // 置位表示「该 close 由我们主动放弃连接引起」（换 token / 已判定失效），
    // 此时不再安排重连 —— 否则会退化成无意义的退避风暴。
    let abandoned = false

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

      // 收敛优先于一切短路：本连接用的凭证若已被裁定失效，而 storage 里又出现了
      // 它（多标签页、别的代码路径写回），这里必须再清一次 —— 否则 apiClient
      // 会继续带着这个已死的 token 发请求，而用户看到的就是「离线且不自愈」。
      // 条件严格到「storage 里的值 === 已裁定失效的那个值」，正常路径下恒为 false。
      if (
        !manuallyClosed.value &&
        invalidatedToken.value !== null &&
        attemptToken === invalidatedToken.value &&
        getToken() === invalidatedToken.value
      ) {
        logger.info('WebSocket 关闭：storage 中残留已失效凭证，收敛清理（不重复探测）')
        clearAuthToken()
        return
      }

      if (manuallyClosed.value || abandoned) {
        return
      }

      // ─ who-am-I 校验：判定这次关闭是否属于「认证失效」（B1）──────
      // 收敛为两问，两问都必须过：
      //   ① 关闭时 storage 里是不是**还是**本次连接用的那个 token？
      //      已经变了就不是本连接的事（用户已重新登录），绝不能拿旧事件清掉新会话；
      //      已经空了说明别处（login 失败 / 退出 / HTTP 拦截器）已经处理过，不重复动作。
      //   ② 该 token 是否真的已被服务端拒绝？—— 必须另发一次权威 HTTP 探测。
      //
      // 之所以坚持探测而不是「连接失败就清 token」：WebSocket 的 error/close
      // 不携带 HTTP 状态码，连接失败在 JS 侧只有一种形状，把「网络抖动」当
      // 「认证失效」处理会把正在正常使用的用户直接踢下线 —— 这是本修复的红线。
      const tokenAtClose = getToken()
      if (tokenAtClose !== attemptToken) {
        logger.info('WebSocket 关闭：凭证已变更或已被清理，交由既有路径处理，不重复动作')
      } else {
        // 先停掉重连计时器：探测是异步的，若期间旧计时器先触发，
        // 会在「会话已失效」被确认之前又开一条注定 401 的连接。
        if (reconnectTimer.value !== null) {
          clearTimeout(reconnectTimer.value)
          reconnectTimer.value = null
        }
        abandoned = true
        void probeSessionValidity().then((verdict) => {
          if (verdict === 'expired') {
            // 走与 client.ts 同一条清理路径；清完 getToken() 为空，
            // connect() early-return，重连自然停止。
            invalidateSession('session_expired')
          } else if (verdict === 'indeterminate') {
            // 网络抖动 / 探测超时 / 服务端 5xx：**不清理任何东西**，照常退避重连。
            abandoned = false
            scheduleReconnect()
          } else {
            // 会话有效却握不上手：属服务端/反代侧问题，同样保持重连。
            logger.warn('WebSocket 关闭但会话仍然有效，继续退避重连')
            abandoned = false
            scheduleReconnect()
          }
        })
        return
      }

      scheduleReconnect()
    }
  }

  // 指数退避重连（原内联在 onclose，抽出为命名函数供探测后的两条分支复用）
  const scheduleReconnect = () => {
    // 指数退避重连。回调里显式复查 manuallyClosed：定时器期间用户可能已退出登录
    if (reconnectTimer.value === null) {
      const delay = Math.min(
        BASE_RECONNECT_DELAY * Math.pow(2, reconnectAttempts.value),
        MAX_RECONNECT_DELAY
      )
      reconnectAttempts.value++
      logger.info(`WebSocket 将在 ${delay / 1000}s 后重连（第 ${reconnectAttempts.value} 次）`)
      reconnectTimer.value = setTimeout(() => {
        reconnectTimer.value = null
        if (manuallyClosed.value) return
        connect()
      }, delay)
    }

    stopHeartbeat()
  }

  /**
   * 凭证变更后的换连接（B1 的自愈面）。
   * 自身不做任何认证判定：新凭证是否有效由服务端握手回答，
   * 判不判失效仍统一走 onclose 的 probeSessionValidity。
   * 已判定失效时直接拒绝（此时 storage 里也不该还有 token）。
   */
  const reconnectWithFreshToken = () => {
    if (isCurrentTokenInvalidated()) {
      logger.debug('WebSocket 换连接被拒绝：当前会话已判定失效')
      return
    }
    const token = getToken()
    if (!token) {
      logger.debug('WebSocket 换连接被拒绝：无 token')
      return
    }
    if (ws.value && ws.value.readyState === WebSocket.OPEN) {
      logger.info('WebSocket 检测到凭证变更，主动重连以携带新凭证')
    }
    connect(token, true)
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
    // ── 会话失效面（B1/B2/B3）──
    sessionInvalidated,
    invalidatedToken,
    isCurrentTokenInvalidated,
    authFailureReason,
    authInvalidationNonce,
    invalidateSession,
    resetAuthStateAfterStaleToken,
    reconnectWithFreshToken,
    getToken,
    subscribe,
    onConnected,
    connect,
    disconnect,
    send
  }
})
