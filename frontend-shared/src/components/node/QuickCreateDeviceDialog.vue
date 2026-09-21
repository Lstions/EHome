<template>
  <el-dialog
    v-model="visible"
    title="创建边缘设备"
    width="560px"
    :close-on-click-modal="false"
    :close-on-press-escape="!submitting"
    :show-close="!submitting"
    class="quick-create-device-dialog"
  >
    <div class="step-tip">
      <el-icon><InfoFilled /></el-icon>
      <span>在节点 <strong>{{ nodeName || nodeId }}</strong> 上直接创建设备，无需预先创建设备配置</span>
    </div>

    <el-form ref="formRef" :model="form" :rules="rules" label-position="top" style="margin-top: 16px;">
      <!-- 设备型号(解析器) -->
      <el-form-item label="设备型号" prop="parserId">
        <el-select
          v-model="form.parserId"
          placeholder="选择设备型号(决定原始字节到物理量的解析方式)"
          filterable
          style="width: 100%;"
          @change="onParserChange"
        >
          <el-option
            v-for="p in availableParsers"
            :key="p.id"
            :label="`${p.name} (${p.id})`"
            :value="p.id"
          >
            <div class="parser-option">
              <span>{{ p.name }}</span>
              <el-tag
                v-for="bus in p.hardware_types"
                :key="bus"
                size="small"
                style="margin-left: 6px;"
              >{{ bus.toUpperCase() }}</el-tag>
            </div>
          </el-option>
        </el-select>
      </el-form-item>

      <!-- 通道(该节点已有通道,按解析器总线类型过滤) -->
      <el-form-item label="通道" prop="channelId">
        <el-select
          v-model="form.channelId"
          placeholder="选择该节点下的已有通道"
          style="width: 100%;"
          :disabled="!form.parserId || channelsLoading"
          :loading="channelsLoading"
        >
          <el-option
            v-for="ch in filteredChannels"
            :key="ch.id"
            :label="`${(ch.hardware_type || '').toUpperCase()} ${ch.hardware_id}${ch.address ? ' / ' + ch.address : ''}`"
            :value="ch.id ?? 0"
          />
        </el-select>
        <!-- R2: 区分"加载中"与"加载完但无匹配通道"两种状态 -->
        <div v-if="form.parserId && channelsLoading" class="channel-hint channel-hint--loading">
          正在加载通道…
        </div>
        <div v-else-if="form.parserId && filteredChannels.length === 0" class="channel-hint">
          该节点暂无匹配 {{ selectedParserBusTypesText }} 的通道，请先在下方"总线配置"中创建
        </div>
      </el-form-item>

      <!-- R1 (2026-09-20 生产故障修复): 通道的 hardware_id 是**总线标识**
           (如 "UART1")，而 EdgeDevice.hardware_id 是**总线上该设备的地址**
           (Modbus 从站号 / I2C 地址，1-254 的十进制或 0xNN)。
           当通道值不是合法设备地址时必须由用户显式填写 —— 否则会把总线名
           原样当设备地址存下去，后端每次派发都在 ParseHardwareAddress 被拒，
           操作静默卡在 QUEUED 直到 deadline 过期(用户只看到"超时")。 -->
      <el-form-item v-if="requiresDeviceAddress" label="设备地址" prop="deviceAddress">
        <el-input
          v-model="form.deviceAddress"
          :placeholder="DEVICE_ADDRESS_PLACEHOLDER"
          clearable
        />
        <div class="channel-hint channel-hint--address">
          所选通道标识「{{ selectedChannel?.hardware_id }}」是总线名称，不是设备地址。
          这里填该设备在总线上的地址（1-254 的十进制或 0xNN）。
        </div>
      </el-form-item>
      <div v-else-if="selectedChannelAddressText" class="channel-hint channel-hint--ok">
        设备地址沿用通道标识「{{ selectedChannelAddressText }}」
      </div>

      <!-- 设备名称 -->
      <el-form-item label="设备名称" prop="name">
        <el-input v-model="form.name" placeholder="请输入边缘设备名称" />
      </el-form-item>

      <!-- EDGE-WIZ-004/005: 驱动声明 schedulable 轮询指令时以逐指令间隔为主配置;
           全局采集间隔仅作后端兼容字段, 隐藏以避免误导。 -->
      <el-form-item v-if="!hasSchedulableCommands" label="采集间隔 (ms)" prop="interval_ms">
        <el-input-number v-model="form.interval_ms" :min="100" :max="3600000" :step="100" />
      </el-form-item>
      <el-alert
        v-else
        type="info"
        :closable="false"
        show-icon
        class="interval-hint"
      >
        <template #title>该设备型号按下方“轮询指令”逐条设置间隔（0 = 禁用）</template>
      </el-alert>

      <!-- EDGE-WIZ-004/005: 逐指令轮询间隔 — 选定型号后拉取驱动 schedulable
           指令; 切换型号自动重置; 加载失败拦截提交。 -->
      <CreateWizardCommandIntervals
        v-if="form.parserId"
        ref="commandIntervalsRef"
        :device-type="form.parserId"
        :saving-disabled="submitting"
        @load-error="handleDriverCommandsLoadError"
      />

      <!-- 继承历史数据 (方案 v3.3 §3.1 入口2: 可折叠区, 默认折叠) -->
      <el-collapse v-model="inheritCollapsed" class="inherit-collapse">
        <el-collapse-item name="inherit">
          <template #title>
            <span class="inherit-collapse-title">
              继承历史数据（可选）
              <el-tag v-if="inheritLogicalDeviceId !== null" size="small" type="success" style="margin-left: 8px;">已选择</el-tag>
            </span>
          </template>
          <div class="inherit-collapse-body">
            <div class="inherit-collapse-tip">若为更换/重建的同一台物理设备，可继承其历史数据；默认作为新设备创建</div>
            <LogicalDeviceCandidateSelect
              v-model="inheritLogicalDeviceId"
              :type="form.parserId"
              :node-id="props.nodeId"
              :hardware-id="selectedChannel?.hardware_id || ''"
              :channel-id="selectedChannel?.id"
              :active="inheritExpanded"
            />
          </div>
        </el-collapse-item>
      </el-collapse>
    </el-form>

    <template #footer>
      <el-button @click="visible = false" :disabled="submitting">取消</el-button>
      <el-button type="primary" :loading="submitting" :disabled="!canSubmit" @click="handleSubmit">
        创建设备
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { InfoFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useParserStore } from '@/stores/parser'
import { edgeDeviceApi } from '@/api/edgeDevice'
import LogicalDeviceCandidateSelect from '@/components/device/LogicalDeviceCandidateSelect.vue'
import CreateWizardCommandIntervals from '@/components/device/CreateWizardCommandIntervals.vue'
import type { Parser } from '@/api/parser'
import type { Channel } from '@/api/channel'
import {
  DEVICE_ADDRESS_ERROR,
  DEVICE_ADDRESS_HINT,
  channelHardwareIdIsDeviceAddress,
  isValidDeviceAddress,
  normalizeDeviceAddressInput,
  parseDeviceAddress,
} from '@/utils/deviceAddress'

// R1: 提示文案与后端错误同口径（Modbus 常见 1；I2C 常见 0x76）。
const DEVICE_ADDRESS_PLACEHOLDER = '设备地址，如 1（Modbus 常见）/ 0x76（I2C 常见）'

export interface QuickCreateDeviceDialogProps {
  modelValue: boolean
  nodeId: string          // 节点物理序列号(EdgeDevice.NodeID)
  nodeName?: string       // 节点显示名
  channels: Channel[]     // 该节点已有通道
  channelsLoading?: boolean // 通道加载中(R2: 区分"加载中"与"无匹配通道")
}

const props = withDefaults(defineProps<QuickCreateDeviceDialogProps>(), {
  nodeName: '',
  channels: () => [],
  channelsLoading: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'created': []
}>()

const parserStore = useParserStore()

const visible = computed({
  get: () => props.modelValue,
  set: (val) => emit('update:modelValue', val),
})

const formRef = ref()
const submitting = ref(false)

const form = reactive({
  parserId: '' as string,
  channelId: undefined as number | undefined,
  name: '',
  interval_ms: 1000,
  // R1: 仅当所选通道标识不是合法设备地址时才需要用户填写。
  deviceAddress: '',
})

const rules = {
  parserId: [{ required: true, message: '请选择设备型号', trigger: 'change' }],
  channelId: [{ required: true, message: '请选择通道', trigger: 'change' }],
  name: [{ required: true, message: '请输入设备名称', trigger: 'blur' }],
  // R1: 地址校验器与后端 ParseHardwareAddress 同口径（见 utils/deviceAddress）。
  deviceAddress: [{
    validator: (_rule: unknown, value: string, callback: (error?: Error) => void) => {
      if (!requiresDeviceAddress.value) {
        callback()
        return
      }
      const trimmed = normalizeDeviceAddressInput(value)
      if (trimmed === '') {
        callback(new Error('请填写设备地址'))
        return
      }
      callback(isValidDeviceAddress(trimmed) ? undefined : new Error(DEVICE_ADDRESS_ERROR))
    },
    trigger: 'blur',
  }],
}

// 可用设备型号(有硬件类型定义的)
const availableParsers = computed(() =>
  parserStore.parsers.filter(p => (p.hardware_types || []).length > 0),
)

const selectedParser = computed<Parser | null>(() =>
  availableParsers.value.find(p => p.id === form.parserId) || null,
)

// 按解析器总线类型过滤该节点通道(hardware_type 大小写归一)
const filteredChannels = computed(() => {
  if (!selectedParser.value) return []
  const buses = (selectedParser.value.hardware_types || []).map(b => b.toLowerCase())
  return props.channels.filter(ch =>
    ch && buses.includes((ch.hardware_type || '').toLowerCase()),
  )
})

const selectedParserBusTypesText = computed(() =>
  (selectedParser.value?.hardware_types || []).map(b => b.toUpperCase()).join('/'),
)

// R2: 通道加载中状态(来自父级 NodeDetail 的 devicesLoading)
const channelsLoading = computed(() => props.channelsLoading)

const selectedChannel = computed<Channel | null>(() =>
  props.channels.find(ch => ch.id === form.channelId) || null,
)

// R1 判定入口：所选通道的 hardware_id 是否**不是**合法设备地址。
//
// 判定口径直接复用 @/utils/deviceAddress（与后端 deviceaction.ParseHardwareAddress
// 同一张表，由 DeviceAddressContractParity.spec.ts 钉死）：
//   "UART1"/"I2C0" 等总线名 → 非法 ⇒ 必须让用户显式填写设备地址；
//   "1"/"0x01" 等合法地址   → 合法 ⇒ 沿用通道值，不新增强制字段。
//
// 已知局限（Lead 已裁决接受）：这是"总线名形态"判定，不是"驱动是否需要地址"判定。
// 纯 I2C 采集设备的通道常是 "I2C0" 这种总线名，因此也会被要求填写一次地址；
// 后端 R2 只对"受控操作真的用地址"的型号做拒绝，所以多问这一句不会导致任何设备
// 创建失败。若要精确到"仅地址化驱动强制"，需在 /device-configs/tree 暴露驱动的
// requires_target_address 能力（本次未扩域）。
const requiresDeviceAddress = computed(() => {
  const ch = selectedChannel.value
  if (!ch) return false
  return !channelHardwareIdIsDeviceAddress(ch.hardware_id)
})

// 通道值本身已是合法地址时，展示沿用提示（含默认地址 1 的归一说明）。
const selectedChannelAddressText = computed(() => {
  const ch = selectedChannel.value
  if (!ch) return ''
  const verdict = parseDeviceAddress(ch.hardware_id)
  if (!verdict.valid) return ''
  return verdict.explicit ? String(ch.hardware_id).trim() : '默认地址 1'
})

// 最终提交的设备地址：合法则用用户输入(trim)，否则为空(由表单校验拦截)。
const resolvedDeviceAddress = computed(() => {
  if (!requiresDeviceAddress.value) {
    return String(selectedChannel.value?.hardware_id ?? '').trim()
  }
  return normalizeDeviceAddressInput(form.deviceAddress)
})

const canSubmit = computed(() =>
  Boolean(form.parserId && form.channelId && form.name.trim()),
)

// 方案 v3.3 §3.1 入口2 — "继承历史数据（可选）"折叠区, 默认折叠。
// el-collapse v-model 为激活 name 数组; 空数组 = 全部折叠。
// active=inheritExpanded 传给候选组件: 折叠时不发请求 (延迟加载)。
const inheritCollapsed = ref<string[]>([])
const inheritExpanded = computed(() => inheritCollapsed.value.includes('inherit'))
const inheritLogicalDeviceId = ref<number | null>(null)

const onParserChange = () => {
  // 切换设备型号时清空已选通道(可能不再匹配)
  form.channelId = undefined
  form.deviceAddress = ''
  // EDGE-WIZ-004/005: 切换型号清理旧型号逐指令间隔快照与加载错误;
  // CreateWizardCommandIntervals 自身监听 deviceType 重置已加载指令, 此处
  // 同步清空提交时使用的快照, 保证重选型号不带旧 intervals。
  commandIntervalsError.value = ''
  commandIntervalsSnapshot.value = null
  commandIntervalsReady.value = false
}

// ---- EDGE-WIZ-004/005: 逐指令轮询间隔 (行为对齐创建向导) ----
// 复用 CreateWizardCommandIntervals: 组件只依赖 deviceType (驱动指令模板),
// 不依赖向导上下文; 有 schedulable 指令时隐藏全局采集间隔, 提交时以快照
// 参与 payload。加载失败时拦截提交, 绝不携带旧驱动数据静默创建。
const commandIntervalsRef = ref()
const commandIntervalsReady = ref(false)
const commandIntervalsSnapshot = ref<Record<string, number> | null>(null)
const commandIntervalsError = ref('')

// 当前型号驱动是否声明了 schedulable 轮询指令 — 有则隐藏全局 interval_ms
const hasSchedulableCommands = computed(() => {
  const el = commandIntervalsRef.value as any
  if (!el || typeof el.schedulableCommands === 'undefined') return false
  return (el?.schedulableCommands || []).length > 0
})

const handleDriverCommandsLoadError = (message: string) => {
  commandIntervalsError.value = message
  commandIntervalsReady.value = false
  commandIntervalsSnapshot.value = null
  feedback.error(message)
}

// 提交时一致快照: 先等驱动指令加载结算, 再读取间隔。
// 加载失败/进行中 → null (不携带任何 intervals, 更不会带旧驱动数据)。
const freezeCommandIntervals = async (): Promise<Record<string, number> | null> => {
  if (!form.parserId) return null
  const el = commandIntervalsRef.value as any
  if (!el || typeof el.whenLoaded !== 'function') {
    return commandIntervalsSnapshot.value
  }
  await el.whenLoaded()
  if (commandIntervalsError.value) return null
  if (el.loadFailed) return null
  if (typeof el.getIntervals !== 'function') return commandIntervalsSnapshot.value
  const intervals = el.getIntervals()
  if (intervals === null) {
    // 无 schedulable 指令: 不携带 intervals, 回退到全局 interval_ms
    commandIntervalsSnapshot.value = null
    commandIntervalsReady.value = true
    return null
  }
  commandIntervalsSnapshot.value = intervals
  commandIntervalsReady.value = true
  return intervals
}

const reset = () => {
  form.parserId = ''
  form.channelId = undefined
  form.name = ''
  form.interval_ms = 1000
  form.deviceAddress = ''
  inheritCollapsed.value = []
  inheritLogicalDeviceId.value = null
  // EDGE-WIZ-004/005: 重置逐指令间隔状态
  commandIntervalsError.value = ''
  commandIntervalsSnapshot.value = null
  commandIntervalsReady.value = false
}

const handleSubmit = async () => {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  const ch = selectedChannel.value
  if (!ch || !selectedParser.value) return

  submitting.value = true
  try {
    // EDGE-WIZ-004/005: 先等驱动指令加载结算并冻结逐指令间隔快照。
    // 加载失败时这里拦截, 不允许携带旧驱动的 intervals 静默提交。
    const frozenCommandIntervals = await freezeCommandIntervals()
    if (commandIntervalsError.value) {
      ElMessage.warning('驱动轮询指令未成功加载，请重试或重新选择设备型号')
      return
    }

    // driver-backed 创建: device_config_id=0(无模板),type=parser.id
    // 方案 v3.3 §3.2/§3.3: 折叠区已选候选时携带 logical_device_id 继承
    // 历史数据; 未选 (默认折叠) 不传, 后端新建逻辑身份。
    // EDGE-WIZ-004: 仅驱动声明 schedulable 指令时携带 command_intervals。
    // R1 主修：提交的 hardware_id 必须是**设备地址**，绝不能直接复制通道的
    // 总线标识（"UART1"）。通道值本身是合法地址时沿用它；否则用用户在
    // "设备地址"字段填写的值（表单校验已保证其合法，这里只 trim）。
    const deviceAddress = resolvedDeviceAddress.value
    if (requiresDeviceAddress.value && deviceAddress === '') {
      // 通道标识不是设备地址时必须显式填写: 空值在后端会被当成"默认地址 1"，
      // 若在这里放行就等于换了一种方式静默猜地址。
      ElMessage.warning('请填写设备地址（' + DEVICE_ADDRESS_HINT + '）')
      return
    }
    if (!isValidDeviceAddress(deviceAddress)) {
      ElMessage.warning(DEVICE_ADDRESS_ERROR)
      return
    }
    await edgeDeviceApi.create({
      name: form.name.trim(),
      node_id: String(props.nodeId),
      channel_id: ch.id,
      hardware_id: deviceAddress,
      type: selectedParser.value.id,
      interval_ms: form.interval_ms,
      ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
      ...(inheritLogicalDeviceId.value !== null
        ? { logical_device_id: inheritLogicalDeviceId.value }
        : {}),
    })
    ElMessage.success('创建成功')
    visible.value = false
    reset()
    emit('created')
  } catch (error: any) {
    feedback.handleError(error, '创建失败')
  } finally {
    submitting.value = false
  }
}

// 切换通道时清空地址输入：上一个通道填的地址对新通道没有意义，
// 留着会让"沿用默认值"与"用户上一条输入"混淆。
watch(() => form.channelId, () => {
  form.deviceAddress = ''
})

watch(() => props.modelValue, (val) => {
  if (val && parserStore.parsers.length === 0) {
    parserStore.fetchParsers()
  }
  // P3: 取消/关闭时重置表单,避免下次打开残留上次输入
  if (!val) {
    reset()
  }
})
</script>

<style scoped>
.step-tip {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  background: var(--el-color-success-light-9);
  border-radius: 6px;
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.parser-option {
  display: flex;
  align-items: center;
}

.channel-hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-color-warning);
}

.channel-hint--loading {
  color: var(--el-text-color-secondary);
}

/* R1: 需要用户填写设备地址时的说明（警示色）与"沿用通道地址"的确认提示 */
.channel-hint--address {
  color: var(--el-color-warning);
}

.channel-hint--ok {
  color: var(--el-text-color-secondary);
}

.interval-hint {
  margin-bottom: 8px;
}

/* 方案 v3.3 §3.1 入口2 — 继承历史数据折叠区 (默认折叠) */
.inherit-collapse {
  margin-top: 4px;
  border-top: none;
}

.inherit-collapse-title {
  display: inline-flex;
  align-items: center;
  font-size: 14px;
}

.inherit-collapse-body {
  padding: 4px 2px 8px;
}

.inherit-collapse-tip {
  margin-bottom: 10px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
