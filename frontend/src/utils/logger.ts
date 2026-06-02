/**
 * 统一日志工具类
 * 将前端日志发送到后端统一收集，同时输出到浏览器控制台
 */

interface LogEntry {
  timestamp: string
  level: 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
  service: string
  message: string
  caller?: string
  fields?: Record<string, unknown>
}

interface LogResponse {
  code: number
  message: string
  count?: number
}

class Logger {
  private buffer: LogEntry[] = []
  private flushInterval = 5000 // 5秒批量发送
  private maxBufferSize = 50 // 最大缓冲数量
  private endpoint = '/api/v1/logs'
  private service = 'frontend'
  private enabled = false // 禁用网络传输，仅输出到控制台
  private timer: ReturnType<typeof setInterval> | null = null

  constructor() {
    // 启动定时刷新
    this.startFlushTimer()
    
    // 页面卸载时发送剩余日志
    if (typeof window !== 'undefined') {
      window.addEventListener('beforeunload', () => {
        this.flush()
      })
      
      // 使用 sendBeacon 确保页面关闭时日志能发送
      window.addEventListener('unload', () => {
        this.flushSync()
      })
    }
  }

  /**
   * 启用/禁用日志发送
   */
  setEnabled(enabled: boolean): void {
    this.enabled = enabled
  }

  /**
   * 设置服务名称
   */
  setService(service: string): void {
    this.service = service
  }

  /**
   * 调试日志
   */
  debug(message: string, fields?: Record<string, unknown>): void {
    this.log('DEBUG', message, fields)
  }

  /**
   * 信息日志
   */
  info(message: string, fields?: Record<string, unknown>): void {
    this.log('INFO', message, fields)
  }

  /**
   * 警告日志
   */
  warn(message: string, fields?: Record<string, unknown>): void {
    this.log('WARN', message, fields)
  }

  /**
   * 错误日志
   */
  error(message: string, fields?: Record<string, unknown>): void {
    this.log('ERROR', message, fields)
  }

  /**
   * 记录日志
   */
  private log(level: LogEntry['level'], message: string, fields?: Record<string, unknown>): void {
    const entry: LogEntry = {
      timestamp: new Date().toISOString(),
      level,
      service: this.service,
      message,
      fields,
    }

    // 获取调用位置
    const caller = this.getCaller()
    if (caller) {
      entry.caller = caller
    }

    // 添加到缓冲区
    this.buffer.push(entry)

    // 同时输出到控制台
    this.consoleLog(entry)

    // 如果缓冲区满了，立即发送
    if (this.buffer.length >= this.maxBufferSize) {
      this.flush()
    }
  }

  /**
   * 输出到浏览器控制台
   */
  private consoleLog(entry: LogEntry): void {
    const prefix = `[${entry.timestamp}] [${entry.level}]`
    const args: unknown[] = [prefix, entry.message]
    
    if (entry.fields && Object.keys(entry.fields).length > 0) {
      args.push(entry.fields)
    }

    switch (entry.level) {
      case 'DEBUG':
        console.debug(...args)
        break
      case 'INFO':
        console.info(...args)
        break
      case 'WARN':
        console.warn(...args)
        break
      case 'ERROR':
        console.error(...args)
        break
    }
  }

  /**
   * 获取调用位置
   */
  private getCaller(): string | undefined {
    const stack = new Error().stack
    if (!stack) return undefined

    const lines = stack.split('\n')
    // 跳过 Error 和 logger 内部调用
    for (let i = 3; i < lines.length; i++) {
      const line = lines[i]
      if (line && !line.includes('logger.ts') && !line.includes('Logger.')) {
        // 提取文件名和行号
        const match = line.match(/(?:at\s+)?(?:.*?\s+\()?(.+?):(\d+):(\d+)\)?/)
        if (match) {
          const [, file, lineNum] = match
          // 简化文件路径
          const shortPath = file.split('/').pop() || file
          return `${shortPath}:${lineNum}`
        }
      }
    }
    return undefined
  }

  /**
   * 启动定时刷新
   */
  private startFlushTimer(): void {
    if (this.timer) {
      clearInterval(this.timer)
    }
    this.timer = setInterval(() => {
      this.flush()
    }, this.flushInterval)
  }

  /**
   * 异步发送日志
   */
  async flush(): Promise<void> {
    if (!this.enabled || this.buffer.length === 0) return

    const logs = [...this.buffer]
    this.buffer = []

    try {
      await this.sendLogs(logs)
    } catch (error) {
      // 发送失败，将日志放回缓冲区
      console.warn('Failed to send logs:', error)
      this.buffer = [...logs, ...this.buffer].slice(0, this.maxBufferSize * 2)
    }
  }

  /**
   * 同步发送日志（使用 sendBeacon）
   */
  private flushSync(): void {
    if (!this.enabled || this.buffer.length === 0) return

    const logs = [...this.buffer]
    this.buffer = []

    try {
      const data = JSON.stringify({ logs })
      navigator.sendBeacon(this.endpoint, data)
    } catch (error) {
      // 忽略错误，页面即将关闭
    }
  }

  /**
   * 发送日志到后端
   */
  private async sendLogs(logs: LogEntry[]): Promise<void> {
    const response = await fetch(this.endpoint, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ logs }),
    })

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    const result: LogResponse = await response.json()
    if (result.code !== 0) {
      throw new Error(result.message)
    }
  }

  /**
   * 立即发送所有日志
   */
  async forceFlush(): Promise<void> {
    await this.flush()
  }
}

// 导出单例
export const logger = new Logger()

// 导出类型
export type { LogEntry, LogResponse }
