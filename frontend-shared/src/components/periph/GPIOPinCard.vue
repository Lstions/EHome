<template>
  <div class="gpio-pin-card" :class="{ 'gpio-offline': offline }">
    <div class="card-header">
      <span class="pin-name">GPIO {{ config.pin }}</span>
      <div class="card-header-right">
        <el-tag size="small" :type="directionTagType">{{ directionLabel }}</el-tag>
        <el-button size="small" text type="danger" class="remove-btn" @click="$emit('remove', config.pin)">✕</el-button>
      </div>
    </div>
    <div v-if="config.label" class="pin-label">{{ config.label }}</div>

    <!-- OUTPUT: ON/OFF/TOGGLE buttons -->
    <template v-if="isOutput">
      <div class="gpio-buttons">
        <el-button size="small" type="success" @click="setLevel(1)" :loading="loading" :disabled="offline">
          ON
        </el-button>
        <el-button size="small" type="danger" @click="setLevel(0)" :loading="loading" :disabled="offline">
          OFF
        </el-button>
        <el-button size="small" @click="toggleLevel" :loading="loading" :disabled="offline">
          TOGGLE
        </el-button>
      </div>
      <div class="gpio-level-indicator">
        <span class="level-dot" :class="{ 'level-high': currentLevel === 1, 'level-low': currentLevel === 0 }"></span>
        <span class="level-text">{{ currentLevel === 1 ? 'HIGH' : currentLevel === 0 ? 'LOW' : '—' }}</span>
      </div>
    </template>

    <!-- INPUT: READ button -->
    <template v-else>
      <div class="gpio-buttons">
        <el-button size="small" type="primary" @click="readLevel" :loading="loading" :disabled="offline">
          READ
        </el-button>
      </div>
      <div class="gpio-level-indicator">
        <span class="level-dot" :class="{ 'level-high': currentLevel === 1, 'level-low': currentLevel === 0 }"></span>
        <span class="level-text">{{ currentLevel === 1 ? 'HIGH' : currentLevel === 0 ? 'LOW' : '—' }}</span>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { gpioApi, type GPIOConfig } from '@/api/periph'

const props = defineProps<{
  config: GPIOConfig
  nodeId: string
  offline?: boolean
}>()

defineEmits<{
  (e: 'remove', pin: number): void
}>()

const loading = ref(false)
// OUTPUT: 用 initial_level 作为初始显示；INPUT: 初始未知，显示 —
const currentLevel = ref<number | null>(
  props.config.direction === 1 ? props.config.initial_level : null
)

const isOutput = computed(() => props.config.direction === 1)

const directionLabel = computed(() => {
  const labels = ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN']
  return labels[props.config.direction] || 'UNKNOWN'
})

const directionTagType = computed(() => {
  return isOutput.value ? 'warning' : 'info'
})

// INPUT 引脚：卡片挂载时自动读取一次当前电平
onMounted(async () => {
  if (!isOutput.value && !props.offline && props.nodeId) {
    try {
      const result = await gpioApi.read(props.nodeId, props.config.pin)
      if (result && typeof result.level === 'number') {
        currentLevel.value = result.level
      }
    } catch {
      // 节点可能离线，静默忽略
    }
  }
})

const setLevel = async (level: 0 | 1) => {
  loading.value = true
  try {
    await gpioApi.set(props.nodeId, props.config.pin, level)
    currentLevel.value = level
    ElMessage.success(`GPIO ${props.config.pin} ${level === 1 ? 'ON' : 'OFF'}`)
  } catch (e: any) {
    ElMessage.error(`GPIO 操作失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

const readLevel = async () => {
  loading.value = true
  try {
    const result = await gpioApi.read(props.nodeId, props.config.pin)
    currentLevel.value = result.level
  } catch (e: any) {
    ElMessage.error(`GPIO 读取失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

const toggleLevel = async () => {
  loading.value = true
  try {
    await gpioApi.toggle(props.nodeId, props.config.pin)
    currentLevel.value = currentLevel.value === 1 ? 0 : 1
    ElMessage.success(`GPIO ${props.config.pin} 已翻转`)
  } catch (e: any) {
    ElMessage.error(`GPIO 翻转失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.gpio-pin-card {
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 140px;
  max-width: 200px;
}
.gpio-pin-card.gpio-offline {
  opacity: 0.5;
  pointer-events: none;
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
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
  font-size: 14px;
}
.pin-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.gpio-buttons {
  display: flex;
  gap: 6px;
}
.gpio-level-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
}
.level-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--el-text-color-disabled);
}
.level-dot.level-high {
  background: #67c23a;
  box-shadow: 0 0 6px rgba(103, 194, 58, 0.5);
}
.level-dot.level-low {
  background: #909399;
}
.level-text {
  font-size: 12px;
  font-family: monospace;
}
</style>
