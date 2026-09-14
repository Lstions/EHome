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
   * 带**操作上下文**的错误提示：`context` 一定出现在消息里。
   *
   * 为什么需要它（本轮 I-1 收敛时实测到的取舍问题）:
   * `handleError` 的第二参数是**兜底**而非前缀 —— `extractErrorMessage` 的优先级是
   * `response.data.message || data.msg || error.message || fallback`，
   * 所以**只要拿到了 message，兜底文案就被整个丢弃**。
   * 而本仓后端**总是**带 `message`（`api/envelope.go:66 Error()` 必写 `Message`）⇒
   * 实际后果是：调用方写的操作上下文（"添加 GPIO 失败" / "删除 PWM 失败"）**几乎永远看不到**，
   * 用户只看到服务端那句话，不知道**是哪个操作**失败了。
   *
   * 这个取舍对"服务端原因是根因"的场景是对的（不被本地前缀稀释），
   * 但对"同一句话会出现在多个操作里"的场景（如 `ChannelPanel` 的添加/更新 GPIO）
   * 必须保留上下文 —— 故提供本出口**显式拼接**，而**不是**去改 `extractErrorMessage`
   * 的既有语义（那会静默改变所有既有调用方的表现）。
   *
   * 用法: 当"哪个操作"对用户有信息量时用它；纯粹的错误透传继续用 `handleError`。
   */
  handleErrorWithContext(error: unknown, context: string, fallback = '操作失败') {
    const detail = extractErrorMessage(error, fallback)
    return this.error(`${context}: ${detail}`)
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
