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
        @configure="pin => emit('configure-gpio', pin)"
        @edit="pin => emit('edit-gpio', pin)"
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
        @configure="hardwareId => emit('configure-pwm', hardwareId)"
        @edit="hardwareId => emit('edit-pwm', hardwareId)"
        @remove="removePwm"
      />
    </section>
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

async function removeGpio(pin: number) {
  try {
    await gpioApi.delete(props.nodeId, pin)
    ElMessage.success(`GPIO ${pin} 已删除`)
    await loadAll()
  } catch (error: unknown) {
    feedback.handleError(error, '删除 GPIO 失败')
  }
}
async function removePwm(hardwareId: string) {
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
