import { ref } from 'vue'
import { authApi } from '@/api/auth'
import { isApiErrorCode } from '@/api/client'
import { newIdempotencyKey } from '@/api/deviceOperation'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { extractErrorMessage } from '@/utils/feedback'

/**
 * 受控操作的派发编排（2026-10-15）。
 *
 * 为什么抽出来：DeviceControlPanel 里这套流程（风险动作先 confirm 拿单次令牌 →
 * 再 create 执行；命中 recent_auth_required 时重验证后**整条重跑**）此前只存在于
 * 那一个组件内部。BMS 页要把 MOS 做成直接开关后，第二个入口也需要完全相同的
 * 安全语义 —— 复制一份就会出现"两条路径的确认/重验证行为不一致"这种最难查的缺陷。
 *
 * ⚠ 关键不变量：确认令牌是**单次**且与 params 摘要绑定的。所以
 *   · 重验证成功后必须**重新走 confirm**（不能复用拿不到的旧令牌）；
 *   · 每次执行用新的 Idempotency-Key（重试不应被当成重复提交丢弃）。
 */
export function useControlAction(getDeviceId: () => number) {
  const store = useDeviceOperationStore()
  const submitting = ref(false)
  const lastError = ref('')
  const reauthVisible = ref(false)
  const reauthPassword = ref('')
  const reauthBusy = ref(false)

  /** 待重试的调用：重验证通过后据此整条重跑 */
  let pending: { actionId: string; params: Record<string, unknown> } | null = null

  /** 后端下发 requires_confirmation；缺失时 fail-closed 视为需要确认。 */
  function needsConfirmation(requires?: boolean) { return requires !== false }

  /**
   * 派发一次受控操作。
   * @returns 是否提交成功（false 时 lastError 已带可读原因）
   */
  async function dispatch(
    actionId: string,
    params: Record<string, unknown>,
    requiresConfirmation: boolean,
  ): Promise<boolean> {
    if (submitting.value) return false
    submitting.value = true
    lastError.value = ''
    pending = { actionId, params }
    try {
      let token = ''
      if (requiresConfirmation) {
        const grant = await store.confirm(getDeviceId(), actionId, params)
        token = grant.token
      }
      await store.create(getDeviceId(), actionId, params, token, '', newIdempotencyKey())
      pending = null
      return true
    } catch (error: unknown) {
      if (isApiErrorCode(error, 'recent_auth_required')) {
        // 不当作失败：提示用户验证身份后继续（窗口 10 分钟，见后端 confirmation.go）
        lastError.value = '为执行该操作，请先验证当前账户密码'
        reauthPassword.value = ''
        reauthVisible.value = true
        return false
      }
      lastError.value = extractErrorMessage(error, '操作未能提交')
      pending = null
      return false
    } finally {
      submitting.value = false
    }
  }

  /** 重验证成功后重跑：必须重新 confirm（令牌单次且绑定 params 摘要）。 */
  async function retryAfterReauth(): Promise<boolean> {
    if (!pending || !reauthPassword.value || reauthBusy.value) return false
    const target = pending
    reauthBusy.value = true
    try {
      await authApi.reauthenticate(reauthPassword.value)
      reauthVisible.value = false
      reauthPassword.value = ''
      return await dispatch(target.actionId, target.params, true)
    } catch (error: unknown) {
      lastError.value = extractErrorMessage(error, '身份验证失败')
      return false
    } finally {
      reauthBusy.value = false
    }
  }

  function cancelReauth() {
    reauthVisible.value = false
    reauthPassword.value = ''
    pending = null
  }

  function clearError() { lastError.value = '' }

  return {
    submitting, lastError, reauthVisible, reauthPassword, reauthBusy,
    dispatch, retryAfterReauth, cancelReauth, clearError, needsConfirmation,
  }
}
