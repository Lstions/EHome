<template>
  <div class="channel-manager">
    <!-- 创建/编辑对话框 -->
    <el-dialog
      v-model="showDialog"
      :title="readonly ? '查看通道' : (editingChannel ? '编辑通道' : '创建通道')"
      width="600px"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitting"
      :show-close="!submitting"
      :before-close="handleDialogClose"
      class="channel-manager-dialog"
      @closed="resetForm"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" :disabled="readonly || submitting">
        <!-- 硬件类型（预选时只读） -->
        <el-form-item label="硬件类型" prop="hardware_type">
          <el-select
            v-model="form.hardware_type"
            placeholder="选择硬件类型"
            style="width: 100%;"
            :disabled="!!presetHardwareType"
            @change="onHardwareTypeChange"
          >
            <!-- 展示名走共享源：value 仍是后端契约用的小写。 -->
            <el-option v-for="bus in HW_TYPES" :key="bus" :label="getHardwareLabel(bus)" :value="bus" />
          </el-select>
        </el-form-item>

        <!-- 硬件ID（预选时只读） -->
        <el-form-item label="硬件资源" prop="hardware_id">
          <el-select
            v-model="form.hardware_id"
            placeholder="选择硬件资源"
            style="width: 100%;"
            :disabled="!!presetHardwareId"
            @change="onHardwareIdChange"
          >
            <el-option
              v-for="hw in availableHardwareList"
              :key="hw.id"
              :label="hw.name || hw.id"
              :value="String(hw.id)"
              :disabled="isOccupiedResource(hw)"
            >
              <span>{{ hw.name || hw.id }}</span>
              <span v-if="hw.pins?.length" style="color: var(--el-text-color-secondary); font-size: 12px; margin-left: 8px;">
                ({{ formatPinsBrief(hw.pins) }})
              </span>
              <!-- 已占用：与后端的引脚仲裁同口径，避免用户填完整张表才被 409 拒 -->
              <span v-if="isOccupiedResource(hw)" style="color: var(--el-color-warning); font-size: 12px; margin-left: 8px;">
                已被其它通道占用
              </span>
            </el-option>
          </el-select>
        </el-form-item>

        <!-- ====== 能力驱动的配置参数 ====== -->
        <!-- 自包含模板片段按域下沉：字段组见 ChannelParamFields.vue（字段顺序/文案/class 原样）。
             扫描请求通过 scan 事件回抛，提交与事务仍由本组件负责。 -->
        <ChannelParamFields
          :form="form"
          :caps="currentCaps"
          :scanning="scanning"
          @scan="scanI2C"
        />

        <!-- 通道名称 -->
        <el-form-item label="通道名称">
          <el-input v-model="form.name" placeholder="可选，留空自动生成" />
        </el-form-item>

        <!-- 状态 -->
        <el-form-item label="状态">
          <el-switch v-model="form.enabled" />
          <span style="margin-left: 8px;">{{ form.enabled ? '启用' : '禁用' }}</span>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button v-if="readonly" type="primary" @click="showDialog = false">关闭</el-button>
        <template v-else>
          <el-button :disabled="submitting" @click="showDialog = false">取消</el-button>
          <el-button type="primary" :loading="submitting" @click="handleSubmit">
            {{ editingChannel ? '保存' : '创建' }}
          </el-button>
        </template>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { ElMessage, type FormInstance } from 'element-plus'
import { type Channel } from '@/api/channel'
import { nodeApi } from '@/api/node'
import { useChannelStore } from '@/stores/channel'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { getHardwareLabel } from '@/utils/hardwareTag'
import ChannelParamFields from './ChannelParamFields.vue'
import { HW_TYPES, type HardwareResource } from './channelFormModel'
import { useChannelManager } from './composables/useChannelManager'
import { buildChannelSubmitBusPayload } from './channelBusConfig'

interface Props {
  collectorId: string | number
  modelValue: boolean
  capabilities?: any
  initialData?: any | null
  /** 预选硬件类型（从硬件卡片点击"创建通道"时传入） */
  presetHardwareType?: string
  /** 预选硬件ID */
  presetHardwareId?: string
  collectorStatus?: string
  readonly?: boolean
  /**
   * 已被**其它已启用通道**占用的硬件资源 id（大小写/别名不敏感地比较）。
   *
   * 为什么必须由调用方传入：本组件只拿到 capabilities（资源清单），
   * 拿不到"哪些资源已经建过通道"。后端已在创建时做引脚仲裁（409），
   * 但让用户填完整张表再被拒是坏体验 —— 这里在下拉框里直接标出来。
   */
  occupiedHardwareIds?: string[]
}

const props = defineProps<Props>()
const channelStore = useChannelStore()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'refresh': []
}>()

const showDialog = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v)
})

const submitting = ref(false)
const scanning = ref(false)
const editingChannel = computed(() => props.initialData ?? null)
const readonly = computed(() => props.readonly ?? false)
const formRef = ref<FormInstance>()
let transactionGeneration = 0

// UART 默认波特率 9600（见 useChannelManager 的 initConfigDefaults 说明：本仓现场设备是 2400/4800/9600）。
// 抽成常量是为了让「表单默认值」与「提交兜底值」不可能再各自漂移成不同数字。
// 定义必须留在本组件：源码门禁 ChannelManagerBusConfigGuard.spec.ts 断言本文件包含该标识；
// 同一常量同时注入表单组合式函数与提交载荷编码，仍然只有一个数字来源。
const DEFAULT_UART_BAUD_RATE = 9600

// 表单状态 / 能力投影 / 编辑态回填 / 预选硬件来自组合式函数（与调用方事务无关的部分）。
// 提交链路（store 写入、会话代际校验、collectorId 绑定）仍留在本组件：
// 它是调用方契约，且 4 个源码门禁断言要求这些标识出现在本文件文本中。
const {
  form,
  rules,
  currentCaps,
  availableHardwareList,
  initConfigDefaults,
  onHardwareTypeChange,
  onHardwareIdChange,
  resetForm,
  hydrateForm,
  applyPreset,
} = useChannelManager(props, { defaultUartBaudRate: DEFAULT_UART_BAUD_RATE })

const handleDialogClose = (done: () => void) => {
  if (submitting.value) return
  done()
}

const assertTransaction = (transaction: number, collectorId: string | number, sessionGeneration: number) => {
  assertSessionGeneration(sessionGeneration)
  if (transaction !== transactionGeneration || props.collectorId !== collectorId || !props.modelValue) {
    throw new Error('通道事务已取消')
  }
}

// 占用集：大小写不敏感（能力里的 id 是 "UART1"，历史数据里出现过 "uart1"）。
const occupiedHardwareSet = computed(() => {
  const set = new Set<string>()
  for (const id of props.occupiedHardwareIds || []) {
    const key = String(id || '').trim().toUpperCase()
    if (key) set.add(key)
  }
  return set
})

/**
 * 该资源是否已被别的通道占用。
 *
 * 编辑态下要排除**自己**：否则打开一条 UART1 通道的编辑框，
 * UART1 会被标成"已占用"，用户连原样保存都做不到。
 *
 * 这段判定必须留在本组件（不随表单下沉）：源码门禁
 * ChannelManagerOccupiedResource.spec.ts 断言本文件包含
 * props.initialData?.hardware_id 且模板对选项应用 :disabled="isOccupiedResource(hw)"。
 */
function isOccupiedResource(hw: HardwareResource): boolean {
  const key = String(hw?.id ?? '').trim().toUpperCase()
  if (!key || !occupiedHardwareSet.value.has(key)) return false
  const editing = String(props.initialData?.hardware_id ?? '').trim().toUpperCase()
  return editing !== key
}

// ====== 标签/格式化辅助 ======

const formatPinsBrief = (pins: any[]) => {
  if (!pins?.length) return ''
  return pins
    .filter((p: any) => p.role && p.role !== 0)
    .map((p: any) => {
      const labels: Record<number, string> = { 1:'TX',2:'RX',3:'SDA',4:'SCL',5:'MOSI',6:'MISO',7:'SCLK',8:'CS',9:'GPIO',10:'CH' }
      return `${labels[p.role]||p.role}=${p.pin}`
    })
    .join(' ')
}

// 这些总线的 bus_config 承载引脚/速率，是后续参数修改（改波特率、重配引脚）的唯一数据源；
// 落空会让后续修改永久失败，故提交前必须能确定对应硬件资源。
const BUS_TYPES_REQUIRING_CONFIG = ['uart', 'i2c', 'spi'] as const
const needsBusConfig = (type: string) => (BUS_TYPES_REQUIRING_CONFIG as readonly string[]).includes(type)

// I2C 扫描
const scanI2C = async () => {
  if (!form.hardware_id) return
  const collectorId = props.collectorId
  const transaction = transactionGeneration
  scanning.value = true
  try {
    const result = await nodeApi.scanI2C(collectorId, form.hardware_id)
    if (transaction !== transactionGeneration || props.collectorId !== collectorId || !props.modelValue) return
    if (result?.devices?.length) {
      ElMessage.success(`发现 ${result.devices.length} 个设备: ${result.devices.join(', ')}`)
      // 自动填入第一个地址
      if (!form.address && result.devices[0]) {
        form.address = result.devices[0]
      }
    } else {
      ElMessage.info('未发现 I2C 设备')
    }
  } catch (e: any) {
    if (transaction !== transactionGeneration || props.collectorId !== collectorId || !props.modelValue) return
    ElMessage.warning('扫描失败: ' + (e.message || '未知错误'))
  } finally {
    if (transaction === transactionGeneration && props.collectorId === collectorId) scanning.value = false
  }
}

const handleSubmit = async () => {
  if (!formRef.value) return
  if (readonly.value) return
  const collectorId = props.collectorId
  const sessionGeneration = getSessionGeneration()
  const transaction = transactionGeneration
  try {
    await formRef.value.validate()
    assertTransaction(transaction, collectorId, sessionGeneration)
    submitting.value = true

    const hw = availableHardwareList.value.find(h => String(h.id) === form.hardware_id)
    // 资源能力缺失时**不再静默产出空 bus_config**（2026-10-03 缺陷 4）。
    // 旧行为：`&& hw` 不成立就跳过整个分支，busConfig 保持空串并成功提交，
    // 结果是一条"以后永远改不了波特率"的通道 —— 用户点「改波特率」才看到
    // 「bus_config 为空」，而那时已经晚了。这里改为提交前明确拦下，
    // 并把可操作的下一步告诉用户（后端 UART 还会兜底补齐，见 ensureUARTBusConfig）。
    //
    // 判据覆盖 uart/i2c/spi 三种**需要 bus_config 才能确定引脚/速率**的总线：
    // 旧写法对三者都是同一个静默空串（只是 UART 的后果最容易被用户看见——
    // 改波特率报错；i2c/spi 的空 bus_config 会直接导致节点端无法解析引脚）。
    // 编辑态不得要求资源能力（2026-10-03 审查发现的 P1 回归）。
    //
    // 原判据只写 `needsBusConfig && !hw`，对**编辑已有通道**同样生效。
    // 而编辑入口（NodeOverview 通道行「配置」）不检查能力是否加载；
    // 能力拉取失败时 capabilities 会被置成 {buses:{}} ⇒ hw 必然找不到 ⇒
    // 用户连"改个通道名 / 改轮询周期"都保存不了。那是把
    // 「改不了波特率」换成了「连名字都改不了」，比原缺陷更糟。
    //
    // 编辑态的 bus_config 是**已存在的**：只要不清空，就不会制造
    // "改不了波特率"的通道。提交时 data.bus_config 仅在能组装出合法值时才带值
    // （见 channelBusConfig.ts 的 uart/i2c/spi 分支），拿不到时保持 undefined
    // ⇒ PUT 不带该字段 ⇒ 后端保留原值。真正的兜底仍在后端 ensureUARTBusConfig。
    //
    // 因此：**只在新建时**要求资源能力可解析。
    const isEditingExistingChannel = Boolean(editingChannel.value?.id)
    if (needsBusConfig(form.hardware_type) && !hw && !isEditingExistingChannel) {
      // 走 feedback 而非裸 ElMessage.error：项目 I-1 静态门禁
      // (utils/__tests__/i1ErrorConvergenceGuard.spec.ts) 要求所有错误提示
      // 经 utils/feedback 收敛，否则样式/级别/埋点会分叉。
      feedback.error(
        `该节点尚未上报所选 ${getHardwareLabel(form.hardware_type)} 资源，无法确定引脚与速率。请等待节点上报后重试，或改用已上报的资源。`,
      )
      submitting.value = false
      return
    }

    // 地址归一 / config 合并 / bus_config 字节编码按域下沉到 channelBusConfig.ts；
    // 默认 UART 波特率由本组件显式传入，保证「表单默认值」与「提交兜底值」同源。
    const { address, config, busConfig } = buildChannelSubmitBusPayload(form, hw, DEFAULT_UART_BAUD_RATE)

    // 后端存储约定为**大写**（实测 channels.hardware_type 只有 'UART'/'I2C'…），
    // 而本表单用的小写（'uart'）。**两个字段必须同约定** —— 此前只把 bus_type 转大写，
    // 于是落库得到 hardware_type='uart' + bus_type='UART' 的混合行，
    // 而 channels 既有行是大写 ⇒ 同一列里混用两种大小写，
    // 使所有按大写比较的读取方（含 ChannelPanel / ChannelTerminal）对这类行静默失配。
    // 后端校验用 EqualFold（大小写不敏感）不会拦这种混合，且落库不归一
    // （handler_device.go:687/755/774）⇒ 必须在这里统一。
    // toUpperCase() 的静态类型是宽 string，而 Channel.hardware_type 只接受枚举联合
    // （'uart' | 'UART' | …）。这里显式收窄为**大写成员**，不是 as any：
    // 值域本就来自 channelFormModel.ts 的 HW_TYPES（小写四值），大写后必然落在联合的大写半边。
    const hardwareType = form.hardware_type.toUpperCase() as Channel['hardware_type']
    const data: Partial<Channel> = {
      node_id: String(collectorId),
      hardware_type: hardwareType,
      hardware_id: form.hardware_id,
      bus_type: hardwareType,
      address: address || undefined,
      name: form.name || undefined,
      enabled: form.enabled,
      bus_config: busConfig || undefined,
      config: JSON.stringify(config)
    }

    if (editingChannel.value?.id) {
      await channelStore.updateChannel(editingChannel.value.id, data)
      assertTransaction(transaction, collectorId, sessionGeneration)
      // 自动同步配置到节点
      try {
        await nodeApi.syncConfig(collectorId)
        assertTransaction(transaction, collectorId, sessionGeneration)
        ElMessage.success('更新成功，配置已同步到节点')
      } catch (syncError: any) {
        if (
          syncError?.message === '会话已变更'
          || transaction !== transactionGeneration
          || props.collectorId !== collectorId
          || !props.modelValue
        ) throw syncError
        ElMessage.warning('更新成功，但配置同步失败：' + (syncError.message || '节点可能离线'))
      }
    } else {
      await channelStore.createChannel(data as any)
      assertTransaction(transaction, collectorId, sessionGeneration)
      // 自动同步配置到节点
      try {
        await nodeApi.syncConfig(collectorId)
        assertTransaction(transaction, collectorId, sessionGeneration)
        ElMessage.success('创建成功，配置已同步到节点')
      } catch (syncError: any) {
        if (
          syncError?.message === '会话已变更'
          || transaction !== transactionGeneration
          || props.collectorId !== collectorId
          || !props.modelValue
        ) throw syncError
        ElMessage.warning('创建成功，但配置同步失败：' + (syncError.message || '节点可能离线'))
      }
    }

    assertTransaction(transaction, collectorId, sessionGeneration)
    submitting.value = false
    showDialog.value = false
    emit('refresh')
  } catch (error: any) {
    if (transaction === transactionGeneration && error !== 'cancel') feedback.handleError(error, '操作失败')
  } finally {
    if (transaction === transactionGeneration) submitting.value = false
  }
}

// ====== 初始化：预选硬件 ======

watch(showDialog, (open) => {
  if (!open) {
    transactionGeneration++
    submitting.value = false
    scanning.value = false
    return
  }

  if (editingChannel.value) {
    // 编辑模式：从通道数据填充（config 归一与 UART bus_config 解码见 useChannelManager.hydrateForm）
    hydrateForm(editingChannel.value)
  } else {
    resetForm()
    // 应用预选硬件类型和ID（从硬件卡片点击"创建通道"时传入）
    applyPreset()
    // 延迟初始化默认值（等 currentCaps 计算完成）
    setTimeout(() => initConfigDefaults(), 50)
  }
})

watch(() => props.collectorId, () => {
  transactionGeneration++
  submitting.value = false
  scanning.value = false
})

onUnmounted(() => {
  transactionGeneration++
  submitting.value = false
  scanning.value = false
})
</script>

<style scoped>
.channel-manager {
  padding: 0;
}

/* 预选硬件时只读字段的视觉提示（对子组件内的 select 同样生效：:deep 从子组件根节点向下匹配） */
:deep(.el-select.is-disabled .el-input__inner) {
  color: var(--text-color-primary);
  -webkit-text-fill-color: var(--text-color-primary);
}
</style>

<!-- Global styles for the el-dialog custom-class (scoped CSS cannot reach dialog overlay) -->
<style>
.channel-manager-dialog {
  display: flex;
  flex-direction: column;
  max-height: 80vh;
}

.channel-manager-dialog .el-dialog__body {
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}

.channel-manager-dialog .el-dialog__footer {
  flex-shrink: 0;
  border-top: 1px solid var(--border-color-light);
  padding: 12px 20px;
}
</style>
