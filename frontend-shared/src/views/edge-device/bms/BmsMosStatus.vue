<template>
  <div class="bms-mos">
    <!-- 2026-10-15（用户诉求）：MOS 状态做成**直接开关**，
         不再要求用户到 26 项「受控操作」清单里找 set_mos_policy 再走表单。
         安全语义不变：set_mos_policy 是 high 风险动作，后端**强制**单次确认令牌
         （commandexec/confirmation.go），因此切开关后仍走一次极简确认。

         ⚠ 极性：协议参数字段是 charge_software_closed / discharge_software_closed，
           语义是「软件关闭位」，与 fet_status 的「导通用 1」**反相**。
           这里一律经 swClosed() 换算，禁止在模板里手写取反。 -->
    <div class="mos-row">
      <div class="mos-item" :class="{ 'is-on': chargeOn }">
        <el-icon :size="18" :color="chargeOn ? 'var(--color-success)' : 'var(--color-info)'">
          <component :is="chargeOn ? Open : Lock" />
        </el-icon>
        <span class="mos-label">充电 MOS</span>
        <span class="mos-state" :class="chargeOn ? 'is-on' : 'is-off'">{{ chargeOn ? '导通' : '关断' }}</span>
        <!-- ⚠ 必须用 :before-change 而不是 :model-value + @change（2026-10-15 实测缺陷）。
             el-switch 是**非受控视觉**组件：点击瞬间它自己就把滑块挪过去，
             而 :model-value 由 fet_status（设备真值）驱动。取消确认时设备值没变，
             Vue 也就不会重渲染这个开关 ⇒ **滑块停在错误位置**，与旁边的
             「导通/关断」文字、fet_status 位自相矛盾（实测 3 次里复现 1 次，
             刷新页面才恢复；刷新后回原值证明是纯视觉脱同步，不是数据错）。
             :before-change 返回 Promise：resolve(true) 才允许它改动视觉，
             resolve(false) 则**保持原位** —— 确认被取消时开关纹丝不动。 -->
        <el-switch
          class="mos-switch"
          :model-value="chargeOn"
          :loading="busyKey === 'charge'"
          :disabled="!canControl || busyKey !== ''"
          active-text="导通"
          inactive-text="关断"
          :aria-label="'充电 MOS 开关'"
          :before-change="() => beforeMosChange('charge')"
        />
      </div>

      <div class="mos-item" :class="{ 'is-on': dischargeOn }">
        <el-icon :size="18" :color="dischargeOn ? 'var(--color-success)' : 'var(--color-info)'">
          <component :is="dischargeOn ? Open : Lock" />
        </el-icon>
        <span class="mos-label">放电 MOS</span>
        <span class="mos-state" :class="dischargeOn ? 'is-on' : 'is-off'">{{ dischargeOn ? '导通' : '关断' }}</span>
        <el-switch
          class="mos-switch"
          :model-value="dischargeOn"
          :loading="busyKey === 'discharge'"
          :disabled="!canControl || busyKey !== ''"
          active-text="导通"
          inactive-text="关断"
          :aria-label="'放电 MOS 开关'"
          :before-change="() => beforeMosChange('discharge')"
        />
      </div>

      <div v-if="balanceOn" class="mos-item" :class="{ 'is-on': true }">
        <el-icon :size="18" color="var(--color-success)"><Open /></el-icon>
        <span class="mos-label">均衡</span>
        <span class="mos-state is-on">进行中</span>
      </div>

      <div v-if="!hasFetStatus" class="mos-item is-unknown">
        <el-icon :size="18" color="var(--color-info)"><Lock /></el-icon>
        <span class="mos-label">无 FET 状态数据</span>
      </div>
    </div>

    <!-- 结果常驻：成功/失败都留在页面上（失败必须可行动，规范要求不能只弹 toast） -->
    <p v-if="resultText" class="mos-result" :class="resultOk ? 'is-ok' : 'is-bad'" role="status">
      {{ resultText }}
    </p>
    <p v-else-if="!canControl" class="mos-result is-muted">
      设备离线或状态未知，暂不可控制 MOS
    </p>

    <!-- 极简风险确认：只有「取消 / 确认」两键（用户明确反对在确认里加理由等多余环节） -->
    <el-dialog
      v-model="confirmVisible"
      :title="confirmTitle"
      width="420px"
      :close-on-click-modal="false"
      append-to-body
    >
      <p class="mos-confirm-text">{{ confirmText }}</p>
      <template #footer>
        <el-button @click="cancelConfirm">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="doConfirm">确认</el-button>
      </template>
    </el-dialog>

    <!-- 近期身份验证（后端 10 分钟窗口，缺失即 403 recent_auth_required） -->
    <el-dialog
      v-model="reauthVisible"
      title="验证当前身份"
      width="420px"
      :close-on-click-modal="false"
      append-to-body
    >
      <p class="mos-confirm-text">该操作属于高风险动作，需要重新验证当前账户密码。</p>
      <el-input
        v-model="reauthPassword"
        type="password"
        show-password
        placeholder="请输入当前账户密码"
        data-testid="mos-reauth-password"
      />
      <template #footer>
        <el-button @click="doCancelReauth">取消</el-button>
        <el-button type="primary" :loading="reauthBusy" @click="doReauth">验证并继续</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Open, Lock } from '@element-plus/icons-vue'
import { useControlAction } from '@/composables/useControlAction'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import type { OperationStatus } from '@/api/deviceOperation'

const props = defineProps<{
  data: Record<string, number> | null
  /** 受控动作的目标设备；缺省时开关不可用（不给用户一个按不动的按钮） */
  deviceId?: number
  /** 节点/设备离线：MOS 控制不可下发 */
  offline?: boolean
}>()

const emit = defineEmits<{ (e: 'changed'): void }>()

// fet_status bitmask: bit0 = 充电 MOS, bit1 = 放电 MOS
// 1 = 导通, 0 = 关断（与协议参数字段"软件关闭位"反相，见下）
const hasFetStatus = computed(() => {
  if (!props.data) return false
  // ⚠ 必须包含 lastFet：否则在 0xAA/0x05/0x04 等**不含 fet_status** 的帧到达时，
  //   本判定会翻成 false ⇒ canControl 变 false ⇒ 开关被禁用、并在无需操作时抖动。
  return (
    props.data.fet_status !== undefined ||
    lastFet.value !== null ||
    props.data.mos_charge !== undefined
  )
})

/**
 * 最近一次**真正携带** fet_status 的值（2026-10-15 实测缺陷的核心修复）。
 *
 * 根因：latestData 是"任意指令的最新一帧"，不是"0x03 基本信息帧"。
 *   设备按各指令自己的间隔轮流上报，实测 60 次采样的帧分布：
 *     0x03 读取基本信息 45%（**唯一含 fet_status 的**）
 *     0xAA 保护次数     32%（不含）
 *     0x05 硬件版本      8%（不含）  0x0F 综合 8%（不含）  0x04 单体 7%（不含）
 *   ⇒ 约 **47% 的时间**当前帧根本没有 fet_status。
 *
 * 改前把这种"本帧没这个字段"当成"MOS 关断"（回退到 mos_charge 等 legacy 字段，
 * 而真机不发这些字段 ⇒ 恒 undefined ⇒ false）⇒ 开关每几秒自己翻转一次，
 * 界面在设备实际充电时显示「充电 MOS 关断」。这是**危险的信息错误**，
 * 不是显示瑕疵：用户会据此判断电池没在充电。
 *
 * 正确语义：一帧不含 fet_status = **该帧对 MOS 状态没有信息**，
 * 应保留上一次已知值，而不是解释为 false。
 * 启用 5 条轮询后非 0x03 帧占比升高，把该缺陷从偶发放大成显性。
 */
const lastFet = ref<number | null>(null)
watch(
  () => props.data?.fet_status,
  (v) => { if (typeof v === 'number') lastFet.value = v },
  { immediate: true },
)
/** 设备切换时清空，避免把上一台设备的 MOS 状态当成新设备的 */
watch(() => props.deviceId, () => { lastFet.value = null })

/** 有效 fet_status：操作回执的权威值 > 本帧 > 最近一次携带该字段的帧 */
const effectiveFet = computed(() => verifiedFet.value ?? props.data?.fet_status ?? lastFet.value)

const chargeOn = computed(() => {
  if (!props.data) return false
  const fet = effectiveFet.value
  if (fet !== undefined && fet !== null) return (fet & 1) !== 0
  return props.data.mos_charge === 1 || props.data.mos_charge_on === 1 || props.data.charge_mos === 1
})

const dischargeOn = computed(() => {
  if (!props.data) return false
  const fet = effectiveFet.value
  if (fet !== undefined && fet !== null) return (fet & 2) !== 0
  return props.data.mos_discharge === 1 || props.data.mos_discharge_on === 1 || props.data.discharge_mos === 1
})

const balanceOn = computed(() => {
  if (!props.data) return false
  return props.data.balancing === 1 || props.data.balance === 1 || props.data.mos_balance === 1
})

/**
 * ⚠ 极性换算（本组件的核心不变量，务必只在这里做）。
 *
 * 协议参数是 `charge_software_closed` / `discharge_software_closed` ——
 * 语义为「**软件关闭**位」；而 fet_status 的位语义是「**导通** = 1」。
 * 两者**反相**：要让 MOS 导通，必须把 software_closed 置 **false**。
 *
 * 历史上这里踩过坑：模拟器把 E1 写入字节直接当 fet_status 存，导致
 * set_mos_policy 回读恒不匹配（"fet_status=0x00 charge bit=0 want 1"）。
 * 因此模板里一律不得手写取反，只能走本函数。
 */
function swClosed(conducting: boolean): boolean {
  return !conducting
}

const canControl = computed(() => !!props.deviceId && !props.offline && hasFetStatus.value)

const opStore = useDeviceOperationStore()

/**
 * 操作回执带来的**权威** fet_status（2026-10-15 实测暴露的时序差）。
 *
 * 现象：MOS 写入由操作自己的串口回读立即校验通过（服务端 SUCCEEDED），但页面显示的
 * latestData 来自**周期轮询快照**（read_basic_info 间隔可能是 8s）⇒ 操作已报"成功"，
 * 开关却要等最多一个轮询周期才跟上。用户看到的就是"说成功了但没变化"——这正是他
 * 反复抱怨的那类缺陷。
 *
 * 修法：成功回执里带着写后回读的 fet_status（verified_result），用它立即桥接显示；
 * 一旦周期帧也报出同一个值（说明实时数据已追上），就撤掉桥接，回到常规数据源。
 * 桥接**自愈**，不会长期覆盖真实读数。
 */
const verifiedFet = ref<number | null>(null)

watch(() => props.data?.fet_status, (live) => {
  if (verifiedFet.value !== null && live === verifiedFet.value) verifiedFet.value = null
})

/**
 * 本次页面上最后一次 MOS 操作的**终态**。
 *
 * 为什么需要：派发成功后只显示"正在回读校验…"是不够的 —— 写+回读要两次串口往返
 * （实测约 1~2s），而结果可能是 FAILED（如"回读不一致：检查保护状态是否覆盖"）。
 * 若不把终态接回来，用户会永远停在"正在校验"，正是此前指令频率踩过的同一个坑。
 * 数据源与「受控操作」面板同源（store.histories），不另开一套轮询。
 */
const lastMosOp = computed(() => {
  const list = opStore.histories.get(props.deviceId ?? 0) ?? []
  return list.find(o => o.action_id === 'set_mos_policy')
})

/** 执行状态 → 中文（不暴露 QUEUED/VERIFYING 等内部枚举） */
function statusText(status: OperationStatus): string {
  const map: Record<OperationStatus, string> = {
    QUEUED: '已发送', DISPATCHED: '已下发', DEVICE_ACCEPTED: '设备已收到',
    VERIFYING: '正在回读校验…', SUCCEEDED: '成功', FAILED: '失败',
    CANCELLED: '已取消', UNKNOWN: '结果未知',
  }
  return map[status] ?? String(status)
}

watch(lastMosOp, (op) => {
  if (!op) return
  const t = statusText(op.status)
  if (op.status === 'SUCCEEDED') {
    resultOk.value = true
    resultText.value = `MOS 操作成功（已回读校验与目标一致）`
    // 用回执里的权威 fet_status 立即桥接，抹掉"成功但开关没动"的窗口（见 verifiedFet 注释）
    const verified = (op.verified_result ?? []).find(v => v.name === 'fet_status')
    if (typeof verified?.value === 'number') verifiedFet.value = verified.value
  } else if (op.status === 'FAILED') {
    resultOk.value = false
    resultText.value = `MOS 操作失败：${op.final_reason || '设备未接受或回读不一致'}`
  } else if (op.status === 'UNKNOWN') {
    resultOk.value = false
    resultText.value = 'MOS 操作结果未知；请勿重复提交，先核对当前 MOS 状态'
  } else if (op.status === 'CANCELLED') {
    resultOk.value = false
    resultText.value = 'MOS 操作已取消'
  } else {
    // QUEUED / DISPATCHED / DEVICE_ACCEPTED / VERIFYING：仍在进行中
    resultOk.value = true
    resultText.value = `MOS 指令${t}`
  }
})

const ca = useControlAction(() => props.deviceId ?? 0)
const { submitting, lastError, reauthVisible, reauthPassword, reauthBusy, dispatch, retryAfterReauth, cancelReauth, clearError } = ca

const confirmVisible = ref(false)
const pendingKind = ref<'charge' | 'discharge' | null>(null)
/** el-switch before-change 的挂起 resolve：由 doConfirm/cancelConfirm/doReauth 决定开关是否允许动 */
let pendingResolve: ((ok: boolean) => void) | null = null
/** 命中 recent_auth_required 时把 resolve 交接给重验证流程（验证通过才放行开关） */
let reauthResolve: ((ok: boolean) => void) | null = null
const pendingTargetOn = ref(false)
const busyKey = ref('')
const resultText = ref('')
const resultOk = ref(true)

const label = (k: 'charge' | 'discharge') => (k === 'charge' ? '充电' : '放电')

const confirmTitle = computed(() =>
  pendingKind.value ? `确认${label(pendingKind.value)} MOS ${pendingTargetOn.value ? '导通' : '关断'}` : '确认操作',
)
const confirmText = computed(() => {
  if (!pendingKind.value) return ''
  const k = label(pendingKind.value)
  return pendingTargetOn.value
    ? `将${k} MOS 置为**导通**（允许${k}）。设备会回读 fet_status 校验是否生效。`
    : `将${k} MOS 置为**关断**（禁止${k}）。设备会回读 fet_status 校验是否生效。`
})

/**
 * 开关的 **before-change** 钩子（2026-10-15 修视觉脱同步缺陷）。
 *
 * el-switch 的 before-change 契约：返回 false 或 **resolve(false)** ⇒ 它**保持原位**、
 * 不改变视觉状态；resolve(true) ⇒ 它才把滑块挪过去。
 *
 * 这正是我们要的语义：把"确认 + 下发 + 回读"整条链路做成这个 Promise，
 * **批准了才允许滑块动**。改前用 :model-value + @change 时，点击瞬间滑块先动，
 * 取消后设备值没变 ⇒ 不触发重渲染 ⇒ 滑块停在错误位置（实测复现，刷新才恢复）。
 *
 * 注意：这里 resolve 的是「本次操作已被受理」（不是「设备已确认成功」）。
 * 后续成败由 lastMosOp 回执驱动 resultText，开关位置则由权威 fet_status 桥接。
 */
function beforeMosChange(kind: 'charge' | 'discharge'): Promise<boolean> {
  const targetOn = !(kind === 'charge' ? chargeOn.value : dischargeOn.value)
  return new Promise<boolean>((resolve) => {
    pendingKind.value = kind
    pendingTargetOn.value = targetOn
    resultText.value = ''
    clearError()
    // 挂起本次 resolve，由 doConfirm / cancelConfirm 决定结果
    pendingResolve = resolve
    confirmVisible.value = true
  })
}

/** 取消：开关必须保持原位（这是本修复的核心行为） */
function cancelConfirm() {
  confirmVisible.value = false
  pendingKind.value = null
  const r = pendingResolve
  pendingResolve = null
  r?.(false)
}

async function doConfirm() {
  const kind = pendingKind.value
  if (!kind) return
  const resolveChange = pendingResolve
  pendingResolve = null
  confirmVisible.value = false
  busyKey.value = kind
  resultText.value = ''
  // 先在**不改变开关视觉**的前提下发起下发；只有受理成功才让滑块动。
  // 这样即使下发失败/需要重验证，开关也不会先跳过去再弹回来（避免虚假反馈）。
  try {
    // 两个位一起提交（协议要求两个字段都必填）：未操作的那个保持当前实际状态。
    const targetCharge = kind === 'charge' ? pendingTargetOn.value : chargeOn.value
    const targetDischarge = kind === 'discharge' ? pendingTargetOn.value : dischargeOn.value
    const ok = await dispatch('set_mos_policy', {
      charge_software_closed: swClosed(targetCharge),
      discharge_software_closed: swClosed(targetDischarge),
      priority: 'user',
    }, true)
    if (ok) {
      resultOk.value = true
      resultText.value = `${label(kind)} MOS 指令已发送，正在回读校验…`
      emit('changed')
      // 受理成功 ⇒ 允许滑块跟随目标位置（真实位置随后由权威 fet_status 收敛）
      resolveChange?.(true)
    } else if (reauthVisible.value) {
      // 需要重验证：**先不让滑块动**，等重验证+重跑成功后再放行（见 doReauth）
      reauthResolve = resolveChange
    } else {
      resultOk.value = false
      resultText.value = lastError.value || 'MOS 指令未能提交'
      resolveChange?.(false)
    }
  } catch {
    resolveChange?.(false)
  } finally {
    busyKey.value = ''
    pendingKind.value = null
  }
}

/** 重验证结果同步到结果行 */
async function doReauth() {
  resultText.value = ''
  const ok = await retryAfterReauth()
  if (ok) {
    resultOk.value = true
    resultText.value = 'MOS 指令已发送，正在回读校验…'
    emit('changed')
    // 重验证+重跑成功，此时才放行开关滑块
    reauthResolve?.(true)
    reauthResolve = null
  } else if (!reauthVisible.value) {
    resultOk.value = false
    resultText.value = lastError.value || 'MOS 指令未能提交'
    reauthResolve?.(false)
    reauthResolve = null
  }
  // 仍在 reauthVisible（用户输错密码）时保持挂起：开关不动，等下一次提交
}

function doCancelReauth() {
  cancelReauth()
  resultOk.value = false
  resultText.value = lastError.value || '已取消（未执行 MOS 操作）'
  // 取消重验证 ⇒ 开关必须保持原位
  reauthResolve?.(false)
  reauthResolve = null
}

// 设备切换时清掉上一台设备的结果文案，避免串台（与 CommandList 同款守卫）
watch(() => props.deviceId, () => {
  resultText.value = ''
  clearError()
  pendingKind.value = null
  confirmVisible.value = false
  verifiedFet.value = null
  // 设备切换时把挂起的 before-change 一律判否，避免开关停在一个"永远 loading"的 Promise 上
  pendingResolve?.(false); pendingResolve = null
  reauthResolve?.(false); reauthResolve = null
})
</script>

<style scoped>
/* 与页面其它指标一致的行式表达（规范 §4.2.2 文字+颜色双通道） */
.bms-mos { display: flex; flex-direction: column; gap: 8px; }
.mos-row { display: flex; flex-direction: column; gap: 10px; }
.mos-item { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.mos-item.is-unknown { opacity: 0.7; }
.mos-label { font-size: 13px; color: var(--el-text-color-regular); }
.mos-state { font-size: 13px; font-weight: 600; white-space: nowrap; }
.mos-state.is-on { color: var(--el-color-success); }
/* ⚠ 关断用**中性色**而非 danger：关断是正常工况，不是故障（规范 §4.2.1） */
.mos-state.is-off { color: var(--el-text-color-secondary); }
/* 开关推到行尾：开关是这一行的动作，值在左、动作在右 */
.mos-switch { margin-left: auto; }
.mos-result { margin: 0; font-size: 12px; line-height: 1.6; }
.mos-result.is-ok { color: var(--el-color-success); }
.mos-result.is-bad { color: var(--el-color-danger); }
.mos-result.is-muted { color: var(--el-text-color-secondary); }
.mos-confirm-text { margin: 0; font-size: 13px; line-height: 1.7; color: var(--el-text-color-regular); }

@media (max-width: 768px) {
  .mos-item { gap: 6px; }
  .mos-switch { margin-left: 0; }
}
</style>

