<!--
  GPIOPinRow.vue — GPIO 行式控制（用于 PinResourceList 内部或独立使用）
  OUTPUT: 一个 el-switch HIGH/LOW
  INPUT: 读取按钮 + 电平显示
  移除配置: 通过 emit 给父组件处理（dropdown + 确认）
  规格: docs/设计/GPIO_PWM_UI重设计规格.md §4
-->
<template>
  <div class="gpio-pin-row" :data-direction="isOutput ? 'output' : 'input'">
    <!-- 电平指示 + 控件 -->
    <div class="gpio-runtime">
      <span class="pin-level-indicator">
        <span class="level-dot" :class="levelDotClass"></span>
        <span class="level-text">{{ levelText }}</span>
      </span>

      <!-- OUTPUT: el-switch -->
      <el-switch
        v-if="isOutput"
        :model-value="currentLevel === 1"
        :loading="loading"
        :disabled="offline || loading"
        active-text="HIGH"
        inactive-text="LOW"
        :aria-label="`GPIO ${config.pin} 输出电平`"
        @change="onSwitchChange"
      />

      <!-- INPUT: 读取按钮 -->
      <el-button
        v-else
        size="small"
        type="primary"
        :loading="loading"
        :disabled="offline || loading"
        :aria-label="`读取 GPIO ${config.pin} 电平`"
        @click="readLevel"
      >
        读取
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { gpioApi, type GPIOConfig } from '@/api/periph'

const props = defineProps<{
  config: GPIOConfig
  nodeId: string
  offline?: boolean
}>()

const emit = defineEmits<{
  (e: 'remove', pin: number): void
  (e: 'level-change', pin: number, level: number | null): void
}>()

const loading = ref(false)
// OUTPUT: 用 initial_level 作为初始显示；INPUT: 初始未知，显示 —
const currentLevel = ref<number | null>(
  props.config.direction === 1 ? props.config.initial_level : null
)

const isOutput = computed(() => props.config.direction === 1)

const levelText = computed(() => {
  if (currentLevel.value === 1) return 'HIGH'
  if (currentLevel.value === 0) return 'LOW'
  return '未知'
})

const levelDotClass = computed(() => {
  if (currentLevel.value === 1) return 'level-high'
  if (currentLevel.value === 0) return 'level-low'
  return 'level-unknown'
})

// INPUT 引脚：挂载时自动读取一次当前电平
onMounted(async () => {
  if (!isOutput.value && !props.offline && props.nodeId) {
    try {
      const result = await gpioApi.read(props.nodeId, props.config.pin)
      if (result && typeof result.level === 'number') {
        currentLevel.value = result.level
        emit('level-change', props.config.pin, result.level)
      }
    } catch {
      // 节点可能离线，静默忽略
    }
  }
})

onUnmounted(() => {
  // 清理：无 timer 需要清除
})

function onSwitchChange(val: boolean | string | number) {
  const level = val ? 1 : 0
  setLevel(level as 0 | 1)
}

async function setLevel(level: 0 | 1) {
  loading.value = true
  const prev = currentLevel.value
  currentLevel.value = level
  try {
    await gpioApi.set(props.nodeId, props.config.pin, level)
    emit('level-change', props.config.pin, level)
    ElMessage.success(`GPIO ${props.config.pin} ${level === 1 ? 'HIGH' : 'LOW'}`)
  } catch (e: any) {
    // 回滚
    currentLevel.value = prev
    ElMessage.error(`GPIO 操作失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

async function readLevel() {
  loading.value = true
  try {
    const result = await gpioApi.read(props.nodeId, props.config.pin)
    currentLevel.value = result.level
    emit('level-change', props.config.pin, result.level)
  } catch (e: any) {
    ElMessage.error(`GPIO 读取失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

defineExpose({ currentLevel, setLevel, readLevel })
</script>

<style scoped>
.gpio-pin-row {
  display: flex;
  align-items: center;
  min-width: 0;
}

.gpio-runtime {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  min-width: 0;
}

.pin-level-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.level-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--el-text-color-disabled);
  flex-shrink: 0;
}

.level-dot.level-high {
  background: var(--el-color-success);
}

.level-dot.level-low {
  background: var(--el-text-color-secondary);
}

.level-dot.level-unknown {
  background: var(--el-text-color-placeholder);
}

.level-text {
  font-size: 13px;
  font-family: monospace;
  white-space: nowrap;
}
</style>
