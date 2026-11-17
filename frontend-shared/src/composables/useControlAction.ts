import { ref } from 'vue'
import { newIdempotencyKey } from '@/api/deviceOperation'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { extractErrorMessage } from '@/utils/feedback'

/**
 * 受控操作的派发编排（2026-10-15）。
 *
 * 为什么抽出来：DeviceControlPanel 里这套流程（风险动作先 confirm 拿单次令牌 →
 * 再 create 执行）此前只存在于那一个组件内部。BMS 页要把 MOS 做成直接开关后，
 * 第二个入口也需要完全相同的安全语义 —— 复制一份就会出现"两条路径的确认行为不一致"
 * 这种最难查的缺陷。
 *
 * 2026-11-15 用户裁定：危险操作只保留「二次点击确认」，**不再要求重新输入账户密码**。
 * 因此原「命中 recent_auth_required → 弹密码框 → 重验证通过后整条重跑」的编排已删除，
 * 后端也不再下发该错误码。若旧后端仍返回 recent_auth_required，这里按**普通错误**展示，
 * 绝不弹密码框。
 *
 * ⚠ 关键不变量：确认令牌是**单次**且与 params 摘要绑定的；每次执行用新的
 * Idempotency-Key（重试不应被当成重复提交丢弃）。
 */
export function useControlAction(getDeviceId: () => number) {
  const store = useDeviceOperationStore()
  const submitting = ref(false)
  const lastError = ref('')

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
    try {
      let token = ''
      if (requiresConfirmation) {
        const grant = await store.confirm(getDeviceId(), actionId, params)
        token = grant.token
      }
      await store.create(getDeviceId(), actionId, params, token, '', newIdempotencyKey())
      return true
    } catch (error: unknown) {
      // 2026-11-15 用户裁定：不再有密码二次确认。旧后端若仍回 recent_auth_required，
      // 也走这条普通错误路径（extractErrorMessage 会给出后端文案），不弹密码框、不自动重跑。
      lastError.value = extractErrorMessage(error, '操作未能提交')
      return false
    } finally {
      submitting.value = false
    }
  }

  function clearError() { lastError.value = '' }

  return { submitting, lastError, dispatch, clearError, needsConfirmation }
}
