<template>
  <div class="pwm-channel-card" :class="{ 'pwm-offline': offline }">
    <div class="card-header">
      <span class="pin-name">PWM (GPIO {{ config.pin }})</span>
      <div class="card-header-right">
        <el-tag size="small" :type="running ? 'success' : 'info'">{{ running ? '运行中' : '已停止' }}</el-tag>
        <el-button size="small" text type="danger" class="remove-btn" @click="$emit('remove', config.pin)">✕</el-button>
      </div>
    </div>
    <div v-if="config.label" class="pin-label">{{ config.label }}</div>

    <!-- Frequency display -->
    <div class="pwm-freq">
      <span class="freq-label">频率</span>
      <span class="freq-value">{{ config.frequency }} Hz</span>
    </div>

    <!-- Duty slider with debounce -->
    <div class="pwm-duty">
      <div class="duty-header">
        <span class="duty-label">占空比</span>
        <span class="duty-value">{{ (localDuty / 100).toFixed(2) }}%</span>
      </div>
      <el-slider
        :model-value="localDuty"
        :min="0"
        :max="10000"
        :step="10"
        :show-tooltip="false"
        :disabled="!running || offline"
        @input="onDutyInput"
        @change="onDutyChange"
      />
    </div>

    <!-- Start/Stop buttons -->
    <div class="pwm-buttons">
      <el-button size="small" type="success" @click="startPwm" :loading="loading" :disabled="running || offline">
        启动
      </el-button>
      <el-button size="small" type="danger" @click="stopPwm" :loading="loading" :disabled="!running || offline">
        停止
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { pwmApi, type PWMConfig } from '@/api/periph'

const props = defineProps<{
  config: PWMConfig
  nodeId: string
  offline?: boolean
}>()

defineEmits<{
  (e: 'remove', pin: number): void
}>()

const loading = ref(false)
const running = ref(false)
const localDuty = ref(props.config.duty)

// Update local duty when config changes
watch(() => props.config.duty, (val) => {
  localDuty.value = val
})

// Slider input: only update local display
const onDutyInput = (val: number) => {
  localDuty.value = val
}

// Slider change (released): send API with debounce
let dutyTimer: ReturnType<typeof setTimeout> | null = null
const onDutyChange = (val: number) => {
  if (dutyTimer) clearTimeout(dutyTimer)
  dutyTimer = setTimeout(async () => {
    try {
      await pwmApi.setDuty(props.nodeId, props.config.pin, val)
    } catch (e: any) {
      ElMessage.error(`PWM 占空比设置失败: ${e?.message || '未知错误'}`)
    }
  }, 300)
}

const startPwm = async () => {
  loading.value = true
  try {
    await pwmApi.start(props.nodeId, props.config.pin)
    running.value = true
    ElMessage.success(`PWM GPIO${props.config.pin} 已启动`)
  } catch (e: any) {
    ElMessage.error(`PWM 启动失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

const stopPwm = async () => {
  loading.value = true
  try {
    await pwmApi.stop(props.nodeId, props.config.pin)
    running.value = false
    ElMessage.success(`PWM GPIO${props.config.pin} 已停止`)
  } catch (e: any) {
    ElMessage.error(`PWM 停止失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.pwm-channel-card {
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
}
.pwm-channel-card.pwm-offline {
  opacity: 0.5;
  pointer-events: none;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 4px;
  min-width: 0;
}
.card-header-right {
  display: flex;
  align-items: center;
  gap: 4px;
}
.remove-btn {
  padding: 0;
  width: 20px;
  height: 20px;
  min-height: 20px;
}
.pin-name {
  font-weight: 600;
  font-size: 13px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pin-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pwm-freq {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
}
.freq-label {
  color: var(--el-text-color-secondary);
}
.freq-value {
  font-family: monospace;
  font-weight: 600;
}
.pwm-duty {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.duty-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
}
.duty-label {
  color: var(--el-text-color-secondary);
}
.duty-value {
  font-family: monospace;
  font-weight: 600;
}
.pwm-buttons {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.pwm-buttons .el-button {
  flex: 1 1 auto;
  min-width: 0;
}
</style>
