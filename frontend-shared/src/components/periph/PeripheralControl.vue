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
        :resources="hardwareGpio"
        :configs="gpioConfigs"
        :node-id="nodeId"
        :offline="offline"
        :loading="loading && !hasLoaded"
        :occupied-pins="gpioOccupiedPins"
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
        @configure="hardwareId => emit('configure-pwm', hardwareId)"
        @edit="hardwareId => emit('edit-pwm', hardwareId)"
        @remove="removePwm"
      />
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
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
    ElMessage.error(`加载外设资源失败: ${error instanceof Error ? error.message : '未知错误'}`)
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
    ElMessage.error(`删除 GPIO 失败: ${error instanceof Error ? error.message : '未知错误'}`)
  }
}
async function removePwm(hardwareId: string) {
  try {
    await pwmApi.delete(props.nodeId, hardwareId)
    ElMessage.success(`${hardwareId} 已删除`)
    await loadAll()
  } catch (error: unknown) {
    ElMessage.error(`删除 PWM 失败: ${error instanceof Error ? error.message : '未知错误'}`)
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
</script>

<style scoped>
.peripheral-control { display: flex; flex-direction: column; gap: 16px; }
.periph-header { display: flex; align-items: center; justify-content: space-between; }
.periph-title { font-weight: 600; font-size: 15px; }
section { display: flex; flex-direction: column; gap: 8px; }
h4 { margin: 0; color: var(--el-text-color-primary); }
</style>
