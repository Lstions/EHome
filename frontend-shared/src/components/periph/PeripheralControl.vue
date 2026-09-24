<template>
  <div class="peripheral-control">
    <div class="periph-header">
      <span class="periph-title">外设控制</span>
      <el-button size="small" :loading="loading" @click="loadAll">
        <el-icon><Refresh /></el-icon>刷新
      </el-button>
    </div>

    <el-alert v-if="loadError" type="error" :closable="false" title="资源数据加载失败">
      <el-button text type="primary" size="small" @click="loadAll">重试</el-button>
    </el-alert>

    <section>
      <h4>GPIO 硬件资源</h4>
      <GPIOResourceList
        ref="gpioResourceList"
        :resources="hardwareGpio"
        :configs="gpioConfigs"
        :node-id="nodeId"
        :offline="offline"
        :loading="loading && !hasLoaded"
        :occupied-pins="gpioOccupiedPins"
        :register-pending="registerPendingGpio"
        @configure="pin => { emit('configure-gpio', pin); openGpioDialog(pin) }"
        @edit="pin => { emit('edit-gpio', pin); openGpioDialog(pin) }"
        @remove="removeGpio"
      />
    </section>

    <section>
      <h4>PWM 硬件资源</h4>
      <PWMResourceList
		ref="pwmResourceList"
        :resources="hardwarePwm"
        :configs="pwmConfigs"
        :node-id="nodeId"
        :offline="offline"
        :loading="loading && !hasLoaded"
        :available-pins="availablePwmPins"
        :register-pending="registerPendingPwm"
        @configure="hardwareId => { emit('configure-pwm', hardwareId); openPwmDialog(hardwareId) }"
        @edit="hardwareId => { emit('edit-pwm', hardwareId); openPwmDialog(hardwareId) }"
        @remove="removePwm"
      />
    </section>

    <!-- 配置创建/编辑对话框（与 ChannelPanel 共用同一份实现，见组件头注释）。
         `@configure-*` 仍向上 emit，供父组件做额外联动；本组件同时直接开对话框，
         使「配置」按钮在存活路径上**真的有落点**（改动前该按钮经 NodeOverview
         只弹"尚未接线"，因为唯一实现被封在死文件 ChannelPanel 中）。 -->
    <PeripheralConfigDialog
      v-model="dialogVisible"
      :kind="dialogKind"
      :editing="dialogEditing"
      :gpio="dialogGpio"
      :pwm="dialogPwm"
      :available-pins="availablePwmPins"
      :max-resolution="dialogMaxResolution"
      :saving="dialogSaving"
      @submit="onDialogSubmit"
      @closed="resetDialogState"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { nodeApi, type GPIOBusResource, type PWMBusResource } from '@/api/node'
import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '@/api/periph'
import { channelApi } from '@/api/channel'
import { enabledChannelPins, type ChannelPinConfig } from '@/utils/channelPins'
import GPIOResourceList from './GPIOResourceList.vue'
import PWMResourceList from './PWMResourceList.vue'
import PeripheralConfigDialog from './PeripheralConfigDialog.vue'
import type { GpioFormModel, PwmFormModel } from './PeripheralConfigDialog.vue'


const props = withDefaults(defineProps<{
  nodeId: string
  offline?: boolean
  /**
   * 写后状态回填的登记回调（GPIO 与 PWM 的 payload 形状不同，故分两个 prop）。
   *
   * 语义必须与 GPIOResourceList/PWMResourceList 内部用法一致：返回 true =
   * 「已登记，稍后由 WS（periph_result）回填运行态」；返回 false / 未提供 =
   * 「降级为静态提示（写入命令已发送，等待设备响应）」。父组件不传时行为与改前完全一致。
   */
  registerPendingGpio?: (payload: { requestId: number; pin: number; action: number }) => boolean
  registerPendingPwm?: (payload: { requestId: number; hardwareId: string; action: number }) => boolean
}>(), { offline: false })
const emit = defineEmits<{
  (event: 'configure-gpio' | 'edit-gpio', pin: number): void
  (event: 'configure-pwm' | 'edit-pwm', hardwareId: string): void
}>()

const loading = ref(false)
const loadError = ref(false)
const hasLoaded = ref(false)
const hardwareGpio = ref<GPIOBusResource[]>([])
const hardwarePwm = ref<PWMBusResource[]>([])
const gpioConfigs = ref<GPIOConfig[]>([])
const pwmConfigs = ref<PWMConfig[]>([])
const pwmResourceList = ref<InstanceType<typeof PWMResourceList> | null>(null)
const gpioResourceList = ref<InstanceType<typeof GPIOResourceList> | null>(null)
let loadGeneration = 0
let disposed = false
const channels = ref<ChannelPinConfig[]>([])

const occupiedBusPins = computed(() => enabledChannelPins(channels.value))
const gpioOccupiedPins = computed(() => {
  const occupied = new Map<number, string>()
  for (const [pin, label] of occupiedBusPins.value) occupied.set(pin, label)
  for (const config of pwmConfigs.value) occupied.set(config.pin, `${config.hardware_id} 输出`)
  return occupied
})
const availablePwmPins = computed(() => {
  const occupied = new Set(occupiedBusPins.value.keys())
  gpioConfigs.value.forEach(config => occupied.add(config.pin))
  pwmConfigs.value.forEach(config => occupied.add(config.pin))
  return hardwareGpio.value
    .map(resource => resource.pin ?? Number(resource.id.match(/\d+/)?.[0]))
    .filter(pin => Number.isInteger(pin) && !occupied.has(pin))
})

const loadAll = async () => {
  const nodeId = props.nodeId
  const generation = ++loadGeneration
  loading.value = true
  loadError.value = false
  try {
    const [capabilities, gpios, pwms, channelRows] = await Promise.all([
      nodeApi.getCapabilities(nodeId),
      gpioApi.list(nodeId),
      pwmApi.list(nodeId),
      channelApi.getList(nodeId),
    ])
    if (disposed || generation !== loadGeneration || props.nodeId !== nodeId) return
    hardwareGpio.value = capabilities.buses?.gpio || []
    hardwarePwm.value = capabilities.buses?.pwm || []
    gpioConfigs.value = gpios
    pwmConfigs.value = pwms
    channels.value = Array.isArray(channelRows) ? channelRows : []
    hasLoaded.value = true
  } catch (error: unknown) {
    if (disposed || generation !== loadGeneration || props.nodeId !== nodeId) return
    loadError.value = true
    feedback.handleError(error, '加载外设资源失败')
  } finally {
    if (!disposed && generation === loadGeneration && props.nodeId === nodeId) loading.value = false
  }
}

// ── 配置创建/编辑对话框 ──────────────────────────────────────────────────
//
// 让「配置」/「编辑」按钮在**存活路径**上真的有落点。改动前这两个按钮经
// NodeOverview 只弹「尚未接线」提示 —— 因为 GPIO/PWM 配置表单的唯一实现被封在
// `ChannelPanel.vue`（只被死文件 NodeDetail 引用）。现改用共享组件
// `PeripheralConfigDialog.vue`，与 ChannelPanel 同形，故将来删除 ChannelPanel
// 不会带走任何能力（C5 从"能力迁移"退化为"纯删除"）。
const dialogVisible = ref(false)
const dialogKind = ref<'gpio' | 'pwm'>('gpio')
const dialogEditing = ref(false)
const dialogSaving = ref(false)
const dialogGpio = ref<GpioFormModel | undefined>(undefined)
const dialogPwm = ref<PwmFormModel | undefined>(undefined)
/** 代际快照：对话框提交是异步的，节点切换后回来的响应不得再改 UI */
let dialogGeneration = 0

const resetDialogState = () => {
  dialogEditing.value = false
  dialogGpio.value = undefined
  dialogPwm.value = undefined
}

/** PWM 分辨率上限取设备能力；缺失时回落 14（与 ChannelPanel 的默认一致） */
const dialogMaxResolution = computed(() => {
  const res = hardwarePwm.value.find(r => r.id === dialogPwm.value?.hardware_id)
  const max = Number((res as { max_resolution?: number } | undefined)?.max_resolution)
  return Number.isInteger(max) && max >= 4 ? max : 14
})

function openGpioDialog(pin: number) {
  const cfg = gpioConfigs.value.find(item => item.pin === pin)
  dialogKind.value = 'gpio'
  dialogEditing.value = !!cfg
  dialogGpio.value = {
    pin,
    direction: cfg?.direction ?? 1,
    initial_level: cfg?.initial_level ?? 0,
    label: cfg?.label || '',
  }
  dialogGeneration = ++loadGeneration
  dialogVisible.value = true
}

function openPwmDialog(hardwareId: string) {
  const cfg = pwmConfigs.value.find(item => item.hardware_id === hardwareId)
  const res = hardwarePwm.value.find(r => r.id === hardwareId)
  dialogKind.value = 'pwm'
  dialogEditing.value = !!cfg
  dialogPwm.value = {
    hardware_id: hardwareId,
    channel: cfg?.channel ?? (Number((res as { channel?: number } | undefined)?.channel) || 0),
    pin: cfg?.pin ?? -1,
    frequency: cfg?.frequency ?? 1000,
    duty: cfg?.duty ?? 0,
    resolution: cfg?.resolution ?? 14,
    auto_start: cfg?.auto_start ?? false,
    label: cfg?.label || '',
  }
  dialogGeneration = ++loadGeneration
  dialogVisible.value = true
}

async function onDialogSubmit(payload: GpioFormModel | PwmFormModel) {
  if (props.offline) {
    ElMessage.warning('节点离线，无法写入配置')
    return
  }
  const nodeId = props.nodeId
  const generation = dialogGeneration
  const editing = dialogEditing.value
  const kind = dialogKind.value
  dialogSaving.value = true
  try {
    if (kind === 'gpio') {
      const form = payload as GpioFormModel
      const body = {
        direction: form.direction,
        initial_level: form.initial_level,
        label: form.label,
      }
      // 与 ChannelPanel 同语义：编辑改 pin 自身，创建带 pin
      if (editing) await gpioApi.update(nodeId, form.pin, body)
      else await gpioApi.create(nodeId, { pin: form.pin, ...body })
    } else {
      const form = payload as PwmFormModel
      // **不要提交 channel**：后端 `handler_periph.go:697` 用
      // `Channel: resource.Channel`（从设备上报的资源解析），**忽略**请求体里的 channel。
      // 旧实现 ChannelPanel 也刻意省略它（其 spec 有专门用例
      // `omits the reported PWM channel from create payload because the backend resolves it`）。
      // 传一个会被忽略的字段会让读者以为它生效，故这里显式不带。
      const body = {
        pin: form.pin,
        frequency: form.frequency,
        duty: form.duty,
        resolution: form.resolution,
        auto_start: form.auto_start,
        label: form.label,
      }
      if (editing) await pwmApi.update(nodeId, form.hardware_id, body)
      else await pwmApi.create(nodeId, { hardware_id: form.hardware_id, ...body })
    }
    if (generation !== loadGeneration || props.nodeId !== nodeId) return
    ElMessage.success(editing ? '配置已更新' : '配置已添加')
    dialogVisible.value = false
    await loadAll()
  } catch (error: unknown) {
    if (generation !== loadGeneration || props.nodeId !== nodeId) return
    const msg = (error as { message?: string })?.message || ''
    // 409/pin 冲突是**正常业务结果**，不是异常：与 ChannelPanel.vue:1213 同一判据，
    // 否则用户看到红色报错而不是"该引脚已有配置"。
    if (!editing && /already configured|Conflict|409/i.test(msg)) {
      ElMessage.warning(kind === 'gpio' ? '该引脚已存在配置' : '该 PWM 资源已存在配置')
      dialogVisible.value = false
      await loadAll()
    } else {
      feedback.handleError(error, `${editing ? '更新' : '添加'}配置失败`)
    }
  } finally {
    if (generation === loadGeneration && props.nodeId === nodeId) dialogSaving.value = false
  }
}

async function removeGpio(pin: number) {
  // G10：**移除配置是破坏性且不可逆**——后端 `handler_periph.go:511` 直接
  // `db.Delete(&cfg)` 硬删该行，并向设备下发 `GPIOActionDeconfig` 解除引脚配置。
  // 改前该动作**没有任何确认**：用户点「移除配置」即静默删库 + 解绑设备引脚。
  // 注意这是本轮 E1 接线**新暴露出来**的路径（改前整条链不可达，所以从未有人点到）。
  const ok = await feedback.confirmDanger(
    `移除 GPIO ${pin} 的配置？该配置将被删除，并向节点下发解绑指令（引脚将被释放）。`,
    { title: '确认移除 GPIO 配置', confirmText: '移除', cancelText: '取消' },
  )
  if (!ok) return
  try {
    await gpioApi.delete(props.nodeId, pin)
    ElMessage.success(`GPIO ${pin} 已删除`)
    await loadAll()
  } catch (error: unknown) {
    feedback.handleError(error, '删除 GPIO 失败')
  }
}
async function removePwm(hardwareId: string) {
  // G10：同上，PWM 侧同样是硬删 + 设备侧 DECONFIG（`handler_periph.go` 的 pwm delete 分支）。
  const ok = await feedback.confirmDanger(
    `移除 ${hardwareId} 的 PWM 配置？该配置将被删除，并向节点下发解绑指令（通道将被释放）。`,
    { title: '确认移除 PWM 配置', confirmText: '移除', cancelText: '取消' },
  )
  if (!ok) return
  try {
    await pwmApi.delete(props.nodeId, hardwareId)
    ElMessage.success(`${hardwareId} 已删除`)
    await loadAll()
  } catch (error: unknown) {
    feedback.handleError(error, '删除 PWM 失败')
  }
}

onMounted(() => {
  void loadAll()
})
watch(() => props.nodeId, () => {
  loadGeneration++
  void loadAll()
})
onUnmounted(() => {
  disposed = true
  loadGeneration++
})

/**
 * 把写后回填的入口暴露给父级：父组件收到 WS periph_result 后调用这里，
 * 由本组件转发到对应的行列表（两个子列表各自已 defineExpose）。
 * 这样「WS 订阅与 request_id 代际校验」留在父组件（与 ChannelPanel 一致），
 * 本组件只负责加载与转发，不引入第二套 pending 状态。
 */
defineExpose({
  applyRuntimeLevel: (pin: number, level: number | null) => gpioResourceList.value?.applyRuntimeLevel(pin, level),
  applyRuntimeState: (hardwareId: string, running: boolean | null, duty?: number) => pwmResourceList.value?.applyRuntimeState(hardwareId, running, duty),
  reload: () => loadAll(),
})
</script>

<style scoped>
.peripheral-control { display: flex; flex-direction: column; gap: 16px; }
.periph-header { display: flex; align-items: center; justify-content: space-between; }
.periph-title { font-weight: 600; font-size: 15px; }
section { display: flex; flex-direction: column; gap: 8px; }
h4 { margin: 0; color: var(--el-text-color-primary); }
</style>
