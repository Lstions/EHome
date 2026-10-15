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
        <el-switch
          class="mos-switch"
          :model-value="chargeOn"
          :loading="busyKey === 'charge'"
          :disabled="!canControl || busyKey !== ''"
          active-text="导通"
          inactive-text="关断"
          :aria-label="'充电 MOS 开关'"
          @change="(v: string | number | boolean) => requestChange('charge', v === true)"
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
          @change="(v: string | number | boolean) => requestChange('discharge', v === true)"
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
  return props.data.fet_status !== undefined || props.data.mos_charge !== undefined
})

/** 有效 fet_status：优先用操作回执的权威值，其次用周期帧 */
const effectiveFet = computed(() => verifiedFet.value ?? props.data?.fet_status)

const chargeOn = computed(() => {
  if (!props.data) return false
  const fet = effectiveFet.value
  if (fet !== undefined) return (fet & 1) !== 0
  return props.data.mos_charge === 1 || props.data.mos_charge_on === 1 || props.data.charge_mos === 1
})

const dischargeOn = computed(() => {
  if (!props.data) return false
  const fet = effectiveFet.value
  if (fet !== undefined) return (fet & 2) !== 0
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

/** 切换开关：先弹极简确认（高风险动作后端强制单次令牌，无法跳过） */
function requestChange(kind: 'charge' | 'discharge', targetOn: boolean) {
  pendingKind.value = kind
  pendingTargetOn.value = targetOn
  resultText.value = ''
  clearError()
  confirmVisible.value = true
}

function cancelConfirm() {
  confirmVisible.value = false
  pendingKind.value = null
}

async function doConfirm() {
  const kind = pendingKind.value
  if (!kind) return
  confirmVisible.value = false
  busyKey.value = kind
  resultText.value = ''
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
    } else if (!reauthVisible.value) {
      resultOk.value = false
      resultText.value = lastError.value || 'MOS 指令未能提交'
    }
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
  } else if (!reauthVisible.value) {
    resultOk.value = false
    resultText.value = lastError.value || 'MOS 指令未能提交'
  }
}

function doCancelReauth() {
  cancelReauth()
  resultOk.value = false
  resultText.value = lastError.value || '已取消（未执行 MOS 操作）'
}

// 设备切换时清掉上一台设备的结果文案，避免串台（与 CommandList 同款守卫）
watch(() => props.deviceId, () => {
  resultText.value = ''
  clearError()
  pendingKind.value = null
  confirmVisible.value = false
  verifiedFet.value = null
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

