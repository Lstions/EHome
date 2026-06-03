/**
 * 统一操作反馈
 *
 * 设计原则:
 * - 成功 / 失败 / 警告 / 信息 四种类型，每种一个函数
 * - 错误消息优先从 error.response?.data?.message 提取
 * - 默认 3 秒自动关闭（错误 5 秒）
 * - 底层调用 Element Plus 的 ElMessage，保证风格统一
 */
import { ElMessage, ElMessageBox, type MessageOptions } from 'element-plus'

const DEFAULT_SUCCESS_OPTS: MessageOptions = { type: 'success', duration: 3000, showClose: true }
const DEFAULT_ERROR_OPTS: MessageOptions = { type: 'error', duration: 5000, showClose: true, dangerouslyUseHTMLString: false }
const DEFAULT_WARNING_OPTS: MessageOptions = { type: 'warning', duration: 4000, showClose: true }
const DEFAULT_INFO_OPTS: MessageOptions = { type: 'info', duration: 3000, showClose: true }

/** 从任意 error 对象中提取可展示的消息 */
export function extractErrorMessage(error: unknown, fallback = '操作失败'): string {
  if (!error) return fallback
  if (typeof error === 'string') return error
  if (error instanceof Error) {
    // axios 风格
    const anyErr = error as any
    return (
      anyErr.response?.data?.message ||
      anyErr.response?.data?.msg ||
      anyErr.message ||
      fallback
    )
  }
  return fallback
}

export const feedback = {
  success(message: string, options: MessageOptions = {}) {
    return ElMessage({ message, ...DEFAULT_SUCCESS_OPTS, ...options })
  },
  error(message: string, options: MessageOptions = {}) {
    return ElMessage({ message, ...DEFAULT_ERROR_OPTS, ...options })
  },
  warning(message: string, options: MessageOptions = {}) {
    return ElMessage({ message, ...DEFAULT_WARNING_OPTS, ...options })
  },
  info(message: string, options: MessageOptions = {}) {
    return ElMessage({ message, ...DEFAULT_INFO_OPTS, ...options })
  },

  /** 错误对象统一处理：自动提取 message + 用 error 级别弹出 */
  handleError(error: unknown, fallback = '操作失败') {
    const msg = extractErrorMessage(error, fallback)
    return this.error(msg)
  },

  /**
   * 二次确认弹窗 (危险操作)
   * @returns 用户确认返回 true，取消返回 false
   */
  async confirmDanger(
    message: string,
    options: {
      title?: string
      confirmText?: string
      cancelText?: string
    } = {}
  ): Promise<boolean> {
    try {
      await ElMessageBox.confirm(message, options.title ?? '确认操作', {
        confirmButtonText: options.confirmText ?? '确定',
        cancelButtonText: options.cancelText ?? '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger',
        draggable: true,
      })
      return true
    } catch {
      return false
    }
  },

  /**
   * 普通确认弹窗
   */
  async confirm(
    message: string,
    options: {
      title?: string
      confirmText?: string
      cancelText?: string
    } = {}
  ): Promise<boolean> {
    try {
      await ElMessageBox.confirm(message, options.title ?? '提示', {
        confirmButtonText: options.confirmText ?? '确定',
        cancelButtonText: options.cancelText ?? '取消',
        type: 'info',
        draggable: true,
      })
      return true
    } catch {
      return false
    }
  },
}

export default feedback
