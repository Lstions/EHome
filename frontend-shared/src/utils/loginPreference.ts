/**
 * 登录偏好持久化（「记住我」+ 已记住的用户名）
 *
 * 为什么需要独立模块（而不是塞进 user store / loginLockout）：
 *   1. 语义不同：loginLockout 是**失败惩罚**状态（应随锁定解除而清空），
 *      本模块是**用户意愿**（勾了就一直是勾的），两者生命周期相反，
 *      混在一个 key 里会导致「锁定解除时把用户勾选也清掉」。
 *   2. 需要被登录页与 store 同时使用，且必须能在**没有后端**的情况下
 *      被单测直接覆盖（localStorage 在 happy-dom 下可用）。
 *
 * 与 token 存储的关系（2026-10-20 复核）：
 *   rememberMe 决定 token 落 localStorage（免登录）还是 sessionStorage（关浏览器即失效），
 *   这件事由 stores/user.ts 负责；本模块只负责「表单意愿」，不碰 token。
 *   两处必须使用**同一个** isRemembered() 判定，否则会出现
 *   「复选框是勾的、token 却在 sessionStorage」的错位。
 */

const REMEMBER_KEY = 'login_remember_me'
const USERNAME_KEY = 'login_remembered_username'

/** localStorage 在隐私模式/被禁用时会抛错，全部读写都要兜住 —— 记住我绝不能成为登录的阻塞项 */
function safeGet(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function safeSet(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* 存储不可用：静默降级为「不记住」，不影响登录 */
  }
}

function safeRemove(key: string): void {
  try {
    localStorage.removeItem(key)
  } catch {
    /* 同上 */
  }
}

/** 读取「记住我」勾选状态（上次登录时的意愿） */
export function isRemembered(): boolean {
  return safeGet(REMEMBER_KEY) === '1'
}

/** 读取上次记住的用户名；从未记住过则返回空串 */
export function getRememberedUsername(): string {
  return safeGet(USERNAME_KEY) ?? ''
}

/**
 * 持久化登录意愿。
 * · remember=true  → 记住勾选 + 记住用户名（用户名便于下次预填）
 * · remember=false → 一并清除（用户已明确表示不想被记住，不能残留 PII）
 */
export function persistLoginPreference(remember: boolean, username?: string): void {
  if (!remember) {
    clearLoginPreference()
    return
  }
  safeSet(REMEMBER_KEY, '1')
  if (username !== undefined) {
    safeSet(USERNAME_KEY, username)
  }
}

/** 清除全部偏好（登出时**不**调用：登出 ≠ 不想被记住，见 stores/user.logout 说明） */
export function clearLoginPreference(): void {
  safeRemove(REMEMBER_KEY)
  safeRemove(USERNAME_KEY)
}
