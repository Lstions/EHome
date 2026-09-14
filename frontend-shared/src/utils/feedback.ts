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

/** confirmDanger 专属类名：让焦点兜底能确定性地定位到本次确认框。 */
const DANGER_BOX_CLASS = 'el-message-box--danger-confirm'

/**
 * 把危险确认框的初始焦点**确定地**放到安全侧（取消键）。
 *
 * EP 2.14.3 实测事实 —— 一个 confirm 框的 DOM 里共有 3 个 button，顺序固定为：
 *   [0] .el-message-box__headerbtn              （标题栏右上角关闭，showClose 默认 true）
 *   [1] .el-message-box__btns button  → 取消键   （cancel 先渲染）
 *   [2] .el-message-box__btns button  → 确认键   （confirm 后渲染）
 * （真实 Chromium 复现：tools/f5-focus-probe.mjs 的 A 段。）
 * 因此**不能**用“第一个 button”定位取消键——那是标题栏关闭键；
 * 必须限定 .el-message-box__btns 取第一个。
 *
 * autofocus 三种取值的**实测落点**（Chromium 1440x900，与 jsdom 一致）：
 * - autofocus: true（EP 默认）→ 焦点 = 「确定/删除」按钮，**回车即删除**。
 *   这是审计 F5 指出的诱导性确认；未迁移的裸 ElMessageBox.confirm 就是这一档
 *   （真实浏览器复现：tools/f5-default-autofocus-probe.mjs，activeElement = 「确定」）。
 * - autofocus: false          → 焦点 = 对话框根 div，**既不在危险键上，也不在安全键上**；
 *   且落点随环境漂移：jsdom 里 el-focus-trap 的 tryFocus(根) 失败会退回
 *   focusStartEl='first'，焦点落到 DOM 第一个可聚焦元素（实测是标题栏关闭键）。
 * - 显式聚焦取消键（本函数）→ 焦点 = 「取消」，跨环境稳定。
 * 补充实测：autofocus:false 时真实 Chromium 的 Tab 序列是
 *   根 div → 关闭键 → 取消 → 删除（不存在“第一次 Tab 直达删除”），
 * 但也**没有任何一次默认停在安全键上**，故不能算满足规范 §4.3.4，这里显式定焦。
 *
 * @returns 是否已无需再定焦（定焦成功 / 用户已自己把焦点放进按钮区 / 弹窗已关闭）
 */
export function focusDangerConfirmSafeSide(): boolean {
  if (typeof document === 'undefined') return true
  const box = document.querySelector<HTMLElement>(`.${DANGER_BOX_CLASS}`)
  if (!box?.isConnected) return true
  const btnsArea = box.querySelector<HTMLElement>('.el-message-box__btns')
  const cancel = btnsArea?.querySelector<HTMLElement>('button')
  if (!cancel) return true
  // 用户（或 EP）已经把焦点放在按钮区里时不抢焦点：只在焦点还停在弹窗根节点、
  // 关闭键、或弹窗之外这类“不安全落点”时才接管。
  const active = document.activeElement as HTMLElement | null
  if (active && btnsArea!.contains(active)) return true
  cancel.focus()
  return document.activeElement === cancel
}

/**
 * 把“安全侧定焦”排到 EP 自身聚焦流程之后。
 *
 * EP 的聚焦链路全是微任务：visible watcher 设 focusStartRef → focus-trap 的
 * onMounted → startTrap() 内 await nextTick() → 再 nextTick(tryFocus)。
 * jsdom 实测：第 1 个 nextTick 时我们的定焦确实生效，但第 2 个 nextTick 会被
 * EP 的 tryFocus(rootRef) 覆盖回对话框根节点——因此**用 nextTick 定焦是无效的**。
 * 改用宏任务 setTimeout(0) 排到 EP 全部微任务之后，并用有限次重试兜底，
 * 从而不依赖 EP 内部的微任务层数（EP 升级不会失配）。
 * 真实 Chromium 复现：tools/f5-focus-probe.mjs 的 B 段（注入后 activeElement = 「取消」）。
 */
function scheduleDangerSafeSideFocus(attempt = 0) {
  setTimeout(() => {
    if (!focusDangerConfirmSafeSide() && attempt < 3) scheduleDangerSafeSideFocus(attempt + 1)
  }, 0)
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
      const pending = ElMessageBox.confirm(message, options.title ?? '确认操作', {
        confirmButtonText: options.confirmText ?? '确定',
        cancelButtonText: options.cancelText ?? '取消',
        type: 'warning',
        // 破坏性确认键必须是 danger 类目（规范 §3.4.3）：
        // - confirmButtonType 决定按钮语义类型（EP 2.14.3 实测：只给 class 时
        //   按钮同时带 el-button--primary 与 el-button--danger，靠 CSS 层叠兜底，
        //   语义类名是错的）；
        // - confirmButtonClass 保留给按 class 选择器的既有验收断言与主题覆盖。
        confirmButtonType: 'danger',
        confirmButtonClass: 'el-button--danger',
        // 规范 §4.3.4「默认焦点和文案应避免诱导性确认」：焦点必须**确定地**落在安全侧。
        // autofocus 默认 true 时 EP 直接把初始焦点交给确认键（danger=破坏性）——
        // 这正是审计 F5 说的“回车即删除”。故显式关闭，再由下方显式聚焦取消键。
        autofocus: false,
        draggable: true,
        // 给本次确认框一个可确定性定位的类名，供下方聚焦使用（见 focusDangerConfirmSafeSide）。
        customClass: DANGER_BOX_CLASS,
      })
      // 只做焦点定位，不改按钮语义；见 scheduleDangerSafeSideFocus 的实测依据。
      scheduleDangerSafeSideFocus()
      await pending
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
