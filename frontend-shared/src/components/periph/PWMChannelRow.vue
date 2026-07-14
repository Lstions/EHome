<!--
  PWMChannelRow.vue — PWM 行式控制（用于 PinResourceList 内部或独立使用）
  状态 tag (运行中/已停止/未知) + 占空比数值 + slider + 启停按钮
  停止不使用 danger；移除配置通过 emit 给父组件
  规格: docs/设计/GPIO_PWM_UI重设计规格.md §5
-->
<template>
  <div class="pwm-channel-row">
    <!-- 运行状态 tag -->
    <el-tag size="small" :type="runningTagType" effect="plain">
      {{ runningLabel }}
    </el-tag>

    <!-- 占空比数值 -->
    <span class="pwm-duty-value">{{ (localDuty / 100).toFixed(2) }}%</span>

    <!-- Slider -->
    <el-slider
      :model-value="localDuty"
      :min="0"
      :max="10000"
      :step="10"
      :show-tooltip="false"
      :disabled="offline || running !== true || loading"
      :aria-label="`GPIO ${config.pin} PWM 占空比`"
      class="pwm-duty-slider"
      @input="onDutyInput"
      @change="onDutyChange"
    />

    <!-- 启停按钮 -->
    <el-button
      v-if="running !== true"
      size="small"
      type="primary"
      :loading="loading"
      :disabled="offline || loading"
      @click="startPwm"
    >
      启动
    </el-button>
    <el-button
      v-else
      size="small"
      :loading="loading"
      :disabled="offline || loading"
      @click="stopPwm"
    >
      停止
    </el-button>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { pwmApi, type PWMConfig } from '@/api/periph'

const props = defineProps<{
  config: PWMConfig
  nodeId: string
  offline?: boolean
  /** 外部传入的运行状态: true=运行中, false=已停止, null=未知 */
  running?: boolean | null
}>()

const emit = defineEmits<{
  (e: 'remove', pin: number): void
  (e: 'state-change', pin: number, running: boolean | null): void
  (e: 'duty-change', pin: number, duty: number): void
}>()

const loading = ref(false)
// 运行态: 优先使用外部传入值，否则 null（未知），不伪造 false
const running = ref<boolean | null>(props.running ?? null)
const localDuty = ref(props.config.duty)
const serverDuty = ref(props.config.duty)  // 用于失败回滚

// 外部 running prop 变化时同步
watch(() => props.running, (val) => {
  running.value = val ?? null
})

// config.duty 变化时同步
watch(() => props.config.duty, (val) => {
  localDuty.value = val
  serverDuty.value = val
})

const runningLabel = computed(() => {
  if (running.value === true) return '运行中'
  if (running.value === false) return '已停止'
  return '未知'
})

const runningTagType = computed(() => {
  if (running.value === true) return 'success'
  return 'info'
})

// Slider input: 只更新本地显示
function onDutyInput(val: number) {
  localDuty.value = val
}

// Slider change (松手): 300ms 防抖后提交
let dutyTimer: ReturnType<typeof setTimeout> | null = null

function onDutyChange(val: number) {
  if (dutyTimer) clearTimeout(dutyTimer)
  loading.value = true

  dutyTimer = setTimeout(async () => {
    try {
      await pwmApi.setDuty(props.nodeId, props.config.pin, val)
      serverDuty.value = val
      emit('duty-change', props.config.pin, val)
    } catch (e: any) {
      // 回滚到服务端确认值
      localDuty.value = serverDuty.value
      try {
        const state = await pwmApi.getState(props.nodeId, props.config.pin)
        localDuty.value = state.duty
        serverDuty.value = state.duty
      } catch {
        // 保持 serverDuty
      }
      ElMessage.error(`PWM 占空比设置失败: ${e?.message || '未知错误'}`)
    } finally {
      loading.value = false
    }
  }, 300)
}

onUnmounted(() => {
  if (dutyTimer) clearTimeout(dutyTimer)
})

async function startPwm() {
  loading.value = true
  try {
    await pwmApi.start(props.nodeId, props.config.pin)
    running.value = true
    emit('state-change', props.config.pin, true)
    ElMessage.success(`PWM GPIO${props.config.pin} 已启动`)
  } catch (e: any) {
    ElMessage.error(`PWM 启动失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

async function stopPwm() {
  loading.value = true
  try {
    await pwmApi.stop(props.nodeId, props.config.pin)
    running.value = false
    emit('state-change', props.config.pin, false)
    ElMessage.success(`PWM GPIO${props.config.pin} 已停止`)
  } catch (e: any) {
    ElMessage.error(`PWM 停止失败: ${e?.message || '未知错误'}`)
  } finally {
    loading.value = false
  }
}

defineExpose({ running, localDuty, startPwm, stopPwm })
</script>

<style scoped>
.pwm-channel-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  min-width: 0;
}

.pwm-duty-value {
  font-family: monospace;
  font-weight: 600;
  font-size: 13px;
  white-space: nowrap;
  flex-shrink: 0;
}

.pwm-duty-slider {
  width: 180px;
  flex-shrink: 1;
}

@media (max-width: 768px) {
  .pwm-duty-slider {
    width: 100%;
  }
}
</style>
