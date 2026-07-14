<template>
  <div class="peripheral-control">
    <div class="periph-header">
      <span class="periph-title">外设控制</span>
      <el-button size="small" @click="loadAll" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <el-skeleton v-if="loading && gpioConfigs.length === 0 && pwmConfigs.length === 0" :rows="4" animated />

    <template v-else>
      <!-- GPIO Section -->
      <div v-if="gpioConfigs.length > 0" class="periph-section">
        <div class="section-title">GPIO</div>
        <div class="card-grid">
          <GPIOPinCard
            v-for="gc in gpioConfigs"
            :key="`gpio-${gc.pin}`"
            :config="gc"
            :node-id="nodeId"
            :offline="offline"
          />
        </div>
      </div>

      <!-- PWM Section -->
      <div v-if="pwmConfigs.length > 0" class="periph-section">
        <div class="section-title">PWM</div>
        <div class="card-grid">
          <PWMChannelCard
            v-for="pc in pwmConfigs"
            :key="`pwm-${pc.pin}`"
            :config="pc"
            :node-id="nodeId"
            :offline="offline"
          />
        </div>
      </div>

      <!-- Empty state -->
      <el-empty
        v-if="gpioConfigs.length === 0 && pwmConfigs.length === 0"
        description="暂无外设配置"
        :image-size="60"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import GPIOPinCard from './GPIOPinCard.vue'
import PWMChannelCard from './PWMChannelCard.vue'
import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '@/api/periph'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'

const props = defineProps<{
  nodeId: string
  offline?: boolean
}>()

const loading = ref(false)
const gpioConfigs = ref<GPIOConfig[]>([])
const pwmConfigs = ref<PWMConfig[]>([])

const wsStore = useWebSocketStore()
let unsubPeriphResult: (() => void) | null = null

const loadAll = async () => {
  loading.value = true
  try {
    const [gpios, pwms] = await Promise.all([
      gpioApi.list(props.nodeId).catch(() => []),
      pwmApi.list(props.nodeId).catch(() => []),
    ])
    gpioConfigs.value = gpios
    pwmConfigs.value = pwms
  } finally {
    loading.value = false
  }
}

// Listen for periph_result WebSocket events to update UI
const onPeriphResult = (message: WebSocketMessage) => {
  const payload = message.payload as any
  if (!payload || payload.node_id !== props.nodeId) return
  // Refresh data on any periph result
  loadAll()
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
  gap: 16px;
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
.periph-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-secondary);
  border-bottom: 1px solid var(--el-border-color-lighter);
  padding-bottom: 4px;
}
.card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 10px;
}

/* 窄容器：单列 */
@media (max-width: 480px) {
  .card-grid {
    grid-template-columns: 1fr;
    gap: 8px;
  }
}

/* 宽容器：最多 4 列 */
@media (min-width: 900px) {
  .card-grid {
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  }
}
</style>
