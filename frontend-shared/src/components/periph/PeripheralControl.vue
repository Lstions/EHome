<!--
  PeripheralControl.vue — 外设控制（行式资源面板版）
  使用 PinResourceList 组件呈现唯一 pin 列表。
  规格: docs/设计/GPIO_PWM_UI重设计规格.md
-->
<template>
  <div class="peripheral-control">
    <div class="periph-header">
      <span class="periph-title">外设控制</span>
      <el-button size="small" @click="loadAll" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <PinResourceList
      :hardware-gpio="hardwareGpio"
      :gpio-configs="gpioConfigs"
      :pwm-configs="pwmConfigs"
      :node-id="nodeId"
      :offline="offline"
      :initial-loading="loading && gpioConfigs.length === 0 && pwmConfigs.length === 0 && hardwareGpio.length === 0"
      :refreshing="loading"
      :load-error="loadError"
      @configure-gpio="handleConfigureGpio"
      @configure-pwm="handleConfigurePwm"
      @edit-gpio="$emit('edit-gpio', $event)"
      @edit-pwm="$emit('edit-pwm', $event)"
      @remove-gpio="handleRemoveGpio"
      @remove-pwm="handleRemovePwm"
      @refresh="loadAll"
      @retry="loadAll"
      @row-updated="loadAll"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PinResourceList from './PinResourceList.vue'
import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '@/api/periph'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'

const props = defineProps<{
  nodeId: string
  offline?: boolean
}>()

const emit = defineEmits<{
  (e: 'edit-gpio', pin: number): void
  (e: 'edit-pwm', pin: number): void
  (e: 'configure-gpio', pin: number): void
  (e: 'configure-pwm', pin: number): void
}>()

const loading = ref(false)
const loadError = ref(false)
const gpioConfigs = ref<GPIOConfig[]>([])
const pwmConfigs = ref<PWMConfig[]>([])
// PeripheralControl 没有硬件能力清单来源，使用已配置 pin 作为最小数据源
const hardwareGpio = ref<any[]>([])

const wsStore = useWebSocketStore()
let unsubPeriphResult: (() => void) | null = null

const loadAll = async () => {
  loading.value = true
  loadError.value = false
  try {
    const [gpios, pwms] = await Promise.all([
      gpioApi.list(props.nodeId).catch(() => [] as GPIOConfig[]),
      pwmApi.list(props.nodeId).catch(() => [] as PWMConfig[]),
    ])
    gpioConfigs.value = gpios
    pwmConfigs.value = pwms
    // 从已配置中推导硬件行（降级：无独立硬件能力清单时仅展示已配置行）
    const pinSet = new Set<number>()
    for (const g of gpios) pinSet.add(g.pin)
    for (const p of pwms) pinSet.add(p.pin)
    hardwareGpio.value = Array.from(pinSet).map(pin => ({
      id: `GPIO${pin}`,
      pin,
    }))
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

// Listen for periph_result WebSocket events to update UI
const onPeriphResult = (message: WebSocketMessage) => {
  const payload = message.payload as any
  if (!payload || payload.node_id !== props.nodeId) return
  loadAll()
}

function handleConfigureGpio(pin: number) {
  emit('configure-gpio', pin)
}

function handleConfigurePwm(pin: number) {
  emit('configure-pwm', pin)
}

async function handleRemoveGpio(pin: number) {
  if (!props.nodeId) return
  try {
    await gpioApi.delete(props.nodeId, pin)
    ElMessage.success(`GPIO ${pin} 已删除`)
    loadAll()
  } catch (e: any) {
    ElMessage.error('删除 GPIO 失败: ' + (e?.message || '未知错误'))
  }
}

async function handleRemovePwm(pin: number) {
  if (!props.nodeId) return
  try {
    await pwmApi.delete(props.nodeId, pin)
    ElMessage.success(`PWM ${pin} 已删除`)
    loadAll()
  } catch (e: any) {
    ElMessage.error('删除 PWM 失败: ' + (e?.message || '未知错误'))
  }
}

onMounted(() => {
  loadAll()
  if (wsStore.connected) {
    unsubPeriphResult = wsStore.subscribe(WS_EVENT.PERIPH_RESULT, onPeriphResult)
  }
})

onUnmounted(() => {
  if (unsubPeriphResult) unsubPeriphResult()
})
</script>

<style scoped>
.peripheral-control {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.periph-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.periph-title {
  font-weight: 600;
  font-size: 15px;
}
</style>
