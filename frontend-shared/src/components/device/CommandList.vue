<template>
  <div class="command-intervals" :class="{ embedded }" v-if="schedulableCommands.length > 0">
    <h4 class="section-title">轮询指令</h4>
    <p class="section-desc">每条指令可独立设置轮询间隔（0 = 禁用）</p>
    <div class="command-list">
      <div v-for="cmd in schedulableCommands" :key="cmd.id" class="command-item" :class="{ disabled: localIntervals[cmd.id] === 0 }">
        <div class="cmd-info">
          <span class="cmd-name">{{ cmd.name }}</span>
          <el-tag size="small" :type="cmd.type === 'read' ? 'info' : 'warning'">
            {{ cmd.type === 'read' ? '读' : '写' }}
          </el-tag>
          <span class="cmd-hex">0x{{ cmd.cmd_byte.toString(16).padStart(2, '0').toUpperCase() }}</span>
        </div>
        <div class="cmd-desc">{{ cmd.description }}</div>
        <div class="cmd-controls">
          <el-input-number
            v-model="localIntervals[cmd.id]"
            :min="0" :max="60000" :step="1000"
            size="small" controls-position="right" style="width: 140px"
          />
          <span class="interval-unit">ms</span>
          <el-switch v-model="enabledMap[cmd.id]" size="small" @change="onToggle(cmd.id)" />
        </div>
      </div>
    </div>
    <div class="command-actions">
      <el-button type="primary" size="small" :loading="saving" @click="save">保存</el-button>
      <span v-if="saved" class="saved-hint">已保存，正在同步到节点...</span>
    </div>
  </div>
  <div v-else-if="loaded" class="no-commands">
    <el-empty description="该设备没有可配置的轮询指令；一次性读取请使用下方受控操作" :image-size="60" />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type CommandTemplateWithInterval } from '@/api/edgeDevice'

const props = defineProps<{
  deviceId: number
  deviceType: string
  /** 嵌入模式（移动端折叠面板内）：去掉灰色面板底，减少嵌套层级 */
  embedded?: boolean
}>()

const commands = ref<CommandTemplateWithInterval[]>([])
const localIntervals = reactive<Record<string, number>>({})
const enabledMap = reactive<Record<string, boolean>>({})
const saving = ref(false)
const saved = ref(false)
const loaded = ref(false)
let operationGeneration = 0

async function loadCommands() {
  const id = props.deviceId
  const generation = ++operationGeneration
  loaded.value = false
  try {
    const result = await edgeDeviceApi.getCommandIntervals(id)
    if (generation !== operationGeneration || props.deviceId !== id) return
    commands.value = result
    for (const cmd of commands.value) {
      if (cmd.schedulable) {
        localIntervals[cmd.id] = cmd.current_interval_ms
        enabledMap[cmd.id] = cmd.current_interval_ms > 0
      }
    }
  } catch {
    if (generation !== operationGeneration || props.deviceId !== id) return
    commands.value = []
  } finally {
    if (generation === operationGeneration && props.deviceId === id) loaded.value = true
  }
}

const schedulableCommands = computed(() => commands.value.filter(c => c.schedulable))

onMounted(loadCommands)
watch(() => props.deviceId, loadCommands)
onUnmounted(() => { operationGeneration++ })

function onToggle(cmdId: string) {
  if (enabledMap[cmdId]) {
    // Restore to command's default interval from template, or fallback to 5000
    if (!localIntervals[cmdId] || localIntervals[cmdId] <= 0) {
      const cmd = commands.value.find(c => c.id === cmdId)
      localIntervals[cmdId] = cmd?.interval_ms || 5000
    }
  } else {
    localIntervals[cmdId] = 0
  }
}

async function save() {
  const id = props.deviceId
  const generation = operationGeneration
  saving.value = true
  saved.value = false
  try {
    const intervals: Record<string, number> = {}
    for (const cmd of schedulableCommands.value) {
      intervals[cmd.id] = localIntervals[cmd.id]
    }
    await edgeDeviceApi.updateCommandIntervals(id, intervals)
    if (generation !== operationGeneration || props.deviceId !== id) return
    saved.value = true
    ElMessage.success('指令频率已保存，正在同步到节点')
  } catch (e: any) {
    if (generation !== operationGeneration || props.deviceId !== id) return
    ElMessage.error(e?.response?.data?.message || '保存失败')
  } finally {
    if (generation === operationGeneration && props.deviceId === id) saving.value = false
  }
}
</script>

<style scoped>
.command-intervals { margin-top: 16px; padding: 12px; background: var(--el-fill-color-lighter); border-radius: 8px; }
/* 嵌入模式：去掉灰色面板底，指令行直接平铺，减少一层嵌套 */
.command-intervals.embedded { margin-top: 0; padding: 0; background: transparent; border-radius: 0; }
.section-title { margin: 0 0 4px; font-size: 14px; }
.section-desc { margin: 0 0 12px; font-size: 12px; color: var(--el-text-color-secondary); }
.command-list { display: flex; flex-direction: column; gap: 8px; }
.command-item { display: flex; flex-wrap: nowrap; align-items: center; gap: 8px; padding: 8px; background: var(--el-bg-color); border-radius: 4px; overflow: hidden; }
.command-item.disabled { opacity: 0.5; }
.cmd-info { display: flex; align-items: center; gap: 6px; width: 180px; flex-shrink: 0; }
.cmd-name { font-weight: 500; font-size: 13px; }
.cmd-hex { font-family: monospace; font-size: 11px; color: var(--el-text-color-secondary); }
.cmd-desc { flex: 1; font-size: 11px; color: var(--el-text-color-secondary); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cmd-controls { display: flex; align-items: center; gap: 4px; width: 200px; flex-shrink: 0; justify-content: flex-end; }
.interval-unit { font-size: 11px; color: var(--el-text-color-secondary); }
.command-actions { margin-top: 12px; display: flex; align-items: center; gap: 8px; }
.saved-hint { font-size: 12px; color: var(--el-color-success); }
.no-commands { margin-top: 16px; }
</style>
