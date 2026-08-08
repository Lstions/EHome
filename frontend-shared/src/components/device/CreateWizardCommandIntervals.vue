<!--
  CreateWizardCommandIntervals.vue — 创建边缘设备向导专用的逐指令轮询间隔配置 (EDGE-WIZ-004/005)。

  与详情页 CommandList.vue 的区别:
  - 详情页依赖已创建的 deviceId (GET/PUT /edge-devices/:id/commands);
  - 本组件在创建阶段只有 deviceType, 通过 GET /api/v1/drivers/:type/commands
    拉取驱动指令模板, 只展示 schedulable 轮询指令, 由调用方提交时读取
    getIntervals() 参与 create payload。因此本组件不持有保存/异步落库逻辑。

  契约:
  - 切换 deviceType 时按代次 (loadSequence) 重置已加载的指令与间隔, 旧驱动的
    指令/间隔绝不残留;
  - 允许 0 (0 = 禁用该指令的轮询/调度);
  - 加载失败: 组件内展示错误 + "重试"按钮, 同时 emits('load-error', message);
    调用方负责拦截提交 (getIntervals 在失败/加载中/未加载时会返回 null),
    绝不能带着旧驱动的 intervals 静默提交。
-->
<template>
  <div class="create-cmd-intervals">
    <div v-if="loading" class="cmd-loading">正在加载轮询指令…</div>
    <el-alert
      v-else-if="loadFailed"
      type="error"
      :closable="false"
      show-icon
      style="margin-top: 16px;"
    >
      <template #title>
        <div class="cmd-load-error">
          <span>驱动轮询指令加载失败，请重试，或返回上一步重新选择设备型号</span>
          <el-button size="small" type="primary" @click="loadCommands">重试</el-button>
        </div>
      </template>
    </el-alert>
    <template v-else-if="schedulableCommands.length > 0">
      <div class="cmd-section-title">轮询指令</div>
      <p class="cmd-section-desc">该设备型号支持逐指令轮询调度，请为每条指令设置轮询间隔（0 = 禁用）</p>
      <div class="cmd-list">
        <div
          v-for="cmd in schedulableCommands"
          :key="cmd.id"
          class="cmd-item"
          :class="{ disabled: localIntervals[cmd.id] === 0 }"
          :data-cmd-id="cmd.id"
        >
          <div class="cmd-info">
            <span class="cmd-name" :title="cmd.name">{{ cmd.name }}</span>
            <el-tag size="small" :type="cmd.type === 'read' ? 'info' : 'warning'">
              {{ cmd.type === 'read' ? '读' : '写' }}
            </el-tag>
            <span class="cmd-hex">0x{{ hex(cmd.cmd_byte) }}</span>
          </div>
          <div class="cmd-desc">{{ cmd.description }}</div>
          <div class="cmd-controls">
            <el-input-number
              :model-value="localIntervals[cmd.id]"
              :min="0"
              :max="86400000"
              :step="100"
              :disabled="savingDisabled"
              controls-position="right"
              size="small"
              style="width: 150px"
              :aria-label="`${cmd.name} 轮询间隔`"
              @update:model-value="setInterval(cmd.id, $event)"
            />
            <span class="cmd-unit">ms</span>
            <el-switch
              :model-value="(localIntervals[cmd.id] ?? 0) > 0"
              :disabled="savingDisabled"
              size="small"
              :aria-label="`${cmd.name} 启用`"
              @change="onToggle(cmd.id, $event)"
            />
          </div>
        </div>
      </div>
    </template>
    <template v-else-if="loaded">
      <el-alert type="info" :closable="false" show-icon style="margin-top: 16px;">
        <template #title>该设备型号没有可配置的轮询指令，无需逐指令设置间隔</template>
      </el-alert>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { edgeDeviceApi, type CommandTemplate } from '@/api/edgeDevice'

const props = defineProps<{
  deviceType: string
  // 提交期间禁用编辑 (创建进行中)
  savingDisabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'load-error', message: string): void
}>()

const commands = ref<CommandTemplate[]>([])
const localIntervals = reactive<Record<string, number>>({})
const loading = ref(false)
const loaded = ref(false)
const loadFailed = ref(false)
let loadSequence = 0
let currentLoad: Promise<void> | null = null

const schedulableCommands = computed(() => commands.value.filter(c => c.schedulable))

function hex(byte: number): string {
  return (byte & 0xff).toString(16).padStart(2, '0').toUpperCase()
}

async function loadCommands() {
  const type = props.deviceType
  const sequence = ++loadSequence
  loading.value = true
  loaded.value = false
  loadFailed.value = false
  commands.value = []
  clearIntervals()
  const attempt = (async () => {
    try {
      const result = await edgeDeviceApi.getDriverCommands(type)
      if (sequence !== loadSequence || props.deviceType !== type) return
      commands.value = result || []
      for (const cmd of commands.value) {
        if (cmd.schedulable) localIntervals[cmd.id] = cmd.interval_ms ?? 0
      }
      loaded.value = true
    } catch (error: any) {
      if (sequence !== loadSequence || props.deviceType !== type) return
      loadFailed.value = true
      loaded.value = true
      emit('load-error', error?.message || '驱动指令加载失败')
    } finally {
      if (sequence === loadSequence) loading.value = false
    }
  })()
  currentLoad = attempt
  return attempt
}

/** 等待当前这一轮加载结算 (成功或失败), 供提交流程在读取间隔前同步。 */
function whenLoaded(): Promise<void> {
  if (currentLoad) {
    return currentLoad.then(() => undefined)
  }
  return Promise.resolve()
}

function clearIntervals() {
  for (const key of Object.keys(localIntervals)) delete localIntervals[key]
}

function setInterval(cmdId: string, value: number | undefined) {
  if (value === undefined) return
  const normalized = Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0
  localIntervals[cmdId] = normalized
}

function onToggle(cmdId: string, enabled: boolean) {
  if (enabled) {
    // 恢复默认间隔 (模板 interval_ms), 默认 0 时给 5000 兜底
    if (!localIntervals[cmdId] || localIntervals[cmdId] <= 0) {
      const cmd = commands.value.find(c => c.id === cmdId)
      localIntervals[cmdId] = (cmd?.interval_ms && cmd.interval_ms > 0) ? cmd.interval_ms : 5000
    }
  } else {
    localIntervals[cmdId] = 0
  }
}

/**
 * 调用方提交时读取: 所有 schedulable 指令的当前间隔。
 * 加载失败/加载中/未加载完成时返回 null — 调用方必须拦截, 不能携带旧驱动数据提交。
 */
function getIntervals(): Record<string, number> | null {
  if (loading.value || loadFailed.value || !loaded.value || schedulableCommands.value.length === 0) {
    return null
  }
  const intervals: Record<string, number> = {}
  for (const cmd of schedulableCommands.value) {
    intervals[cmd.id] = localIntervals[cmd.id] ?? 0
  }
  return intervals
}

// 切换设备型号: 立即重置 (即使请求未返回, 旧驱动的指令/间隔也不得残留)
watch(() => props.deviceType, () => {
  loadSequence++
  commands.value = []
  clearIntervals()
  loading.value = false
  loaded.value = false
  loadFailed.value = false
  void loadCommands()
})

defineExpose({ getIntervals, whenLoaded, loadFailed, schedulableCommands, setInterval, loadCommands })

void loadCommands()
</script>

<style scoped>
.create-cmd-intervals {
  margin-top: 16px;
  padding: 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
}
.cmd-loading {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  padding: 8px 0;
}
.cmd-section-title {
  margin: 0 0 4px;
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.cmd-section-desc {
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.cmd-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.cmd-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  overflow: hidden;
}
.cmd-item.disabled {
  opacity: 0.55;
}
.cmd-info {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 200px;
  flex-shrink: 0;
  min-width: 0;
}
.cmd-name {
  font-weight: 500;
  font-size: 13px;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.cmd-hex {
  font-family: monospace;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.cmd-desc {
  flex: 1;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cmd-controls {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.cmd-unit {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.cmd-load-error {
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: space-between;
}
</style>
