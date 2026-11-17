import { computed, reactive, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { feedback } from '@/utils/feedback'
import type { DeviceOperation } from '@/api/deviceOperation'
import { hexOf, num } from '../bmsFormat'
import { OPERATION_UI, TERMINAL, readableFinalReason } from './bmsOperationModel'

/**
 * BMS「MOS 直控」状态域（诉求①：像电灯开关一样直接操作）。
 *
 * 为什么单独拆出来：MOS 的状态推导（fet_status 位语义 + 回执桥接）与
 * 受控操作目录/表单/历史**共享同一批回执**，但两者是两个独立数据域：
 *   · 这里只关心 fet_status 的位与写后回读；
 *   · 目录/表单/历史在 useBmsOperations。
 * 交叉点只有两处，都用显式依赖表达（trackCommand / 回执转交），不靠隐式顺序。
 *
 * ⚠ 极性换算（唯一一处，模板/参数里禁止手写取反）：
 *   协议参数字段是 charge_software_closed / discharge_software_closed —— 语义为
 *   「**软件关闭**位」；fet_status 的位语义是「**导通** = 1」，两者**反相**：
 *   要让 MOS 导通，必须把 software_closed 置 **false**。
 */

export interface BmsMosControlDeps {
  deviceId: Ref<number>
  deviceOnline: Ref<boolean>
  latestData: Ref<Record<string, any> | null>
  dispatch: (actionId: string, params: Record<string, unknown>, requiresConfirmation: boolean) => Promise<boolean>
  clearError: () => void
  lastError: Ref<string>
  /**
   * 每个字段最近一次被上报的采集时刻（epoch ms；实现见 useRealtimeData 的 fieldAt）。
   * 用来给"权威回读桥接"设**有效期**：只有**比回读更新**的 fet_status 才能覆盖它。
   * ⚠ 必须是"fet_status 这个字段"的时刻，不能是"最新一帧"的时刻 —— 0xAA/0x04 帧不含
   * fet_status，拿它们的时刻判会把刚确认的开关状态打回旧值（等于把抖动搬回来）。
   */
  fieldReportedAt?: Ref<Record<string, number>>
  /** 页面发起的命令登记（实现在 useBmsOperations，回执由它转交回来） */
  trackCommand: (actionId: string) => void
}

export function useBmsMosControl(deps: BmsMosControlDeps) {
  const { deviceId, deviceOnline, latestData, dispatch, clearError, lastError, fieldReportedAt, trackCommand } = deps

  const mosPhase = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })
  const mosError = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })

  /**
   * 最近一次**真正携带** fet_status 的值（2026-10-15 实测缺陷修复）。
   *
   * 根因：latestData 是"**任意指令的最新一帧**"，不是"0x03 基本信息帧"。
   *   设备按各指令自己的间隔轮流上报，实测 60 次采样的帧分布：
   *     0x03 读取基本信息 45%（唯一含 fet_status）
   *     0xAA 保护次数 32% / 0x05 硬件版本 8% / 0x0F 综合 8% / 0x04 单体 7%（均不含）
   *   ⇒ 约 **47% 的时间**当前帧根本没有 fet_status。
   *
   * 改前把"本帧没这个字段"当成"MOS 关断"（effectiveFet 变 null ⇒ chargeOn=false），
   * 于是开关每几秒自己翻转，并在设备实际充电时显示「充电 MOS 关断」——
   * 这是**危险的信息错误**：用户会据此判断电池没在充电。
   *
   * 正确语义：该帧对 MOS **没有信息** ⇒ 保留上一次已知值，而不是解释为 false。
   * 启用 5 条轮询后非 0x03 帧占比升高，把该缺陷从偶发放大成显性。
   *
   * （另有一个模拟器侧缺陷已同时修复：0x0F 帧曾硬编码 fet_status=0，
   *   而 0x0F 是 0x03 的超集、同样会被解析 ⇒ 两条指令对同一字段给出矛盾取值。）
   */
  const lastFet = ref<number | null>(null)
  watch(() => latestData.value?.fet_status, (v) => {
    const n = num(v)
    if (n !== null) lastFet.value = n
  }, { immediate: true })
  // 设备切换时清空，避免把上一台设备的 MOS 状态当成新设备的
  watch(deviceId, () => { lastFet.value = null })

  /** fetStatus：优先本帧，其次最近一次携带该字段的帧（保证展示的是**已知**状态而非默认值） */
  const fetStatus = computed(() => num(latestData.value?.fet_status) ?? lastFet.value ?? 0)
  const hasFetStatus = computed(() => num(latestData.value?.fet_status) !== null || lastFet.value !== null)
  const fetHex = computed(() => hexOf(fetStatus.value))

  /**
   * 操作回执带来的**权威** fet_status（时序差桥接）。
   * 周期快照最长落后约 8s，若只等快照，会出现「报成功了开关却没动」。成功回执里带着
   * 写后回读的 fet_status，用它立即桥接；一旦周期帧也报出同一个值即撤掉桥接（自愈）。
   *
   * ⚠ 2026-11-17 真机缺陷修复：桥接**不能只靠"值相等"来撤销**。
   *
   * 旧实现（`verifiedFet: number|null` + "实时值等于回读值就撤桥接"）有两个洞：
   *   ① 一帧**比回读更旧**的周期帧（写之前采集、写之后才到）会把开关打回旧状态，
   *      下一帧再跳回来 —— 真机实测的"忽然跳一下打开、再跳回关闭"；
   *   ② 若设备此后一直不回该位（例如被硬件保护覆盖），桥接会**永久**钉住显示值，
   *      保护关断也看不见 —— 这是危险的信息错误。
   *
   * 现在桥接带**采集时刻**：只要出现一帧晚于该次回读的帧，就以实时值为准（live wins）。
   * 时刻来源是 useRealtimeData 的 fieldAt（**按字段**记、单调递增，乱序帧不会把它拉回去）。
   */
  const verifiedFet = ref<{ value: number; at: number } | null>(null)
  watch(() => latestData.value?.fet_status, (live) => {
    const bridge = verifiedFet.value
    if (bridge && num(live) === bridge.value) verifiedFet.value = null
  })
  /** 优先操作回执的权威值（且没有更晚的帧）；否则用 fetStatus（已含 lastFet 兜底，不会因缺字段而塌陷） */
  const effectiveFet = computed(() => {
    const bridge = verifiedFet.value
    // 只有当"fet_status 被重新上报的时刻"晚于这次回读，才让实时值接管
    if (bridge && (fieldReportedAt?.value?.fet_status ?? 0) <= bridge.at) return bridge.value
    return hasFetStatus.value ? fetStatus.value : null
  })
  const chargeOn = computed(() => effectiveFet.value !== null && (effectiveFet.value & 1) !== 0)
  const dischargeOn = computed(() => effectiveFet.value !== null && (effectiveFet.value & 2) !== 0)
  const canControlMos = computed(() => hasFetStatus.value && deviceOnline.value)

  /** 本帧没有 fet_status 时不得把开关当「关断」——这两个位只用于展示与请求参数 */
  function swClosed(conducting: boolean): boolean {
    return !conducting
  }

  let mosKind: 'charge' | 'discharge' | null = null

  /**
   * 在途态看门狗（2026-11-17 真机缺陷的第二道保险）。
   *
   * 「已发送…」是**在途态**：只有回执推进到终态才会撤，且期间开关是 disabled。
   * 回执链路一旦断掉（WS 未连 / 事件丢失 / 后端没广播），用户就被永久钉在"已发送"上，
   * 开关再也点不动 —— 真机实测用户只能手点页头「刷新数据」自救。
   *
   * 所以这里给它**有界**等待：超时即撤掉在途态、恢复可操作，并把"没拿到结果"如实
   * 写进常驻原因（不谎报成功、也不谎报失败 —— 指令很可能已经生效）。
   * 终态回执一旦到达会清掉计时器，正常路径完全不受影响。
   */
  const MOS_RESULT_TIMEOUT_MS = 15_000
  const phaseTimers: Record<'charge' | 'discharge', ReturnType<typeof setTimeout> | null> = { charge: null, discharge: null }

  function clearPhaseTimer(which: 'charge' | 'discharge') {
    const timer = phaseTimers[which]
    if (timer !== null) {
      clearTimeout(timer)
      phaseTimers[which] = null
    }
  }

  function armPhaseTimer(which: 'charge' | 'discharge') {
    clearPhaseTimer(which)
    phaseTimers[which] = setTimeout(() => {
      phaseTimers[which] = null
      // 期间已经拿到终态（phase 被清空）⇒ 什么都不做
      if (mosPhase[which] === '') return
      mosPhase[which] = ''
      mosError[which] = '未收到执行结果（指令可能已生效）；请先核对当前 MOS 状态再操作'
      feedback.warning((which === 'charge' ? '充电 MOS' : '放电 MOS') + '：未收到执行结果，请核对当前状态')
    }, MOS_RESULT_TIMEOUT_MS)
  }

  async function requestMosChange(which: 'charge' | 'discharge', target: boolean) {
    if (mosPhase[which] !== '' || !canControlMos.value) return
    const currentOn = which === 'charge' ? chargeOn.value : dischargeOn.value
    if (currentOn === target) return
    const name = which === 'charge' ? '充电 MOS' : '放电 MOS'
    const action = target ? '导通' : '关断'
    const effect = which === 'charge'
      ? (target ? '导通后允许充电。' : '关断后禁止充电，充电回路会被切断。')
      : (target ? '导通后允许放电。' : '关断后禁止放电，设备会停止输出。')
    // 风险动作的最小确认：只有 确认 / 取消 两键，不填理由、不加必填项。
    const ok = await feedback.confirmDanger(
      '确认将' + name + '设为「' + action + '」？' + effect + '指令下发约需 1~2 秒，完成后自动回读 fet_status 校验。',
      { title: 'MOS 直控 · ' + name + action, confirmText: '确认' + action, cancelText: '取消' },
    )
    if (!ok) return
    await applyMos(which, target)
  }

  async function applyMos(which: 'charge' | 'discharge', target: boolean) {
    const name = which === 'charge' ? '充电 MOS' : '放电 MOS'
    mosPhase[which] = '下发中'
    mosError[which] = ''
    mosKind = which
    armPhaseTimer(which)
    clearError()
    // 两个位一起提交（协议要求两个字段都必填）：未操作的那个保持当前实际状态。
    const ok = await dispatch('set_mos_policy', {
      charge_software_closed: swClosed(which === 'charge' ? target : chargeOn.value),
      discharge_software_closed: swClosed(which === 'discharge' ? target : dischargeOn.value),
      priority: 'user',
    }, true)
    if (ok) {
      mosPhase[which] = '已发送'
      trackCommand('set_mos_policy')
    } else {
      // 2026-11-15 用户裁定：危险操作只保留二次点击确认，不再要求输入账户密码。
      markFailed(which, lastError.value || 'MOS 指令未能提交')
      feedback.error(name + '指令未能提交：' + (lastError.value || '未知原因'))
    }
  }

  const notified = new Set<string>()
  function notifyOnce(op: DeviceOperation, fn: () => void) {
    const key = op.command_id + ':' + op.status
    if (notified.has(key)) return
    notified.add(key)
    fn()
  }

  /**
   * 把一条**set_mos_policy** 回执映射到 MOS 卡片（失败原因常驻，成功用权威回读桥接）。
   * 由 useBmsOperations 在回执到达时转交过来（同一个 activeCommandId 过滤器）。
   */
  function handleReceipt(op: DeviceOperation) {
    const ui = OPERATION_UI[op.status]
    const terminal = TERMINAL.includes(op.status)
    const kind = mosKind
    if (kind) {
      mosPhase[kind] = terminal ? '' : ui.text
      // 终态到了 ⇒ 撤掉看门狗（在途态结束，开关恢复可操作）
      if (terminal) clearPhaseTimer(kind)
    }
    notifyOnce(op, () => {
      const label = kind === 'charge' ? '充电 MOS' : '放电 MOS'
      if (op.status === 'SUCCEEDED') {
        if (kind) mosError[kind] = ''
        // 用回执里的权威 fet_status 立即桥接，抹掉"成功但开关没动"的窗口。
        const verified = (op.verified_result ?? []).find((v) => v.name === 'fet_status')
        if (typeof verified?.value === 'number') {
          // 桥接带上"这次回读发生在什么时候"：回执的 updated_at/completed_at 是服务端时钟，
          // 与帧采集时刻同源（都用服务端时间），可直接比较。
          const at = Date.parse(op.completed_at || op.updated_at || '')
          verifiedFet.value = { value: verified.value, at: Number.isFinite(at) ? at : Date.now() }
        }
        feedback.success(label + '操作成功（已回读校验）')
      } else if (op.status === 'FAILED') {
        const reason = readableFinalReason(op.final_reason) || '设备未接受或回读校验不一致'
        if (kind) mosError[kind] = reason
        feedback.error(label + '操作失败：' + reason)
      } else if (op.status === 'UNKNOWN') {
        if (kind) mosError[kind] = '操作结果未知；请先核对当前 MOS 状态，勿重复提交。'
        feedback.warning(label + '操作结果未知，请核对当前状态')
      } else if (op.status === 'CANCELLED') {
        if (kind) mosError[kind] = '操作已取消'
        feedback.warning(label + '操作已取消')
      }
    })
  }

  /** 命令已按原参数重新提交：置为"已发送"（2026-11-15 起不再有密码重验证入口，保留给重试编排复用） */
  function markSent(which: 'charge' | 'discharge') {
    mosPhase[which] = '已发送'
    armPhaseTimer(which)
  }
  /** 下发失败 / 取消：清掉在途态并常驻原因（2026-11-15 起失败即走这里，不再等密码输入） */
  function markFailed(which: 'charge' | 'discharge', message: string) {
    clearPhaseTimer(which)
    mosPhase[which] = ''
    mosError[which] = message
  }

  /** 设备切换 / 页面刷新前复位（不动 deviceId 监听与 lastFet 的既有语义） */
  function resetMos() {
    clearPhaseTimer('charge')
    clearPhaseTimer('discharge')
    mosPhase.charge = ''
    mosPhase.discharge = ''
    mosError.charge = ''
    mosError.discharge = ''
    mosKind = null
    verifiedFet.value = null
    notified.clear()
  }

  return {
    mosPhase,
    mosError,
    hasFetStatus,
    fetHex,
    chargeOn,
    dischargeOn,
    canControlMos,
    requestMosChange,
    handleReceipt,
    markSent,
    markFailed,
    resetMos,
  }
}
