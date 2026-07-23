<template>
  <div class="command-intervals" :class="{ embedded }" v-if="commands.length > 0">
    <!-- 轮询指令 -->
    <template v-if="schedulableCommands.length > 0">
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
    </template>

    <!-- 触发指令 -->
    <template v-if="triggerCommands.length > 0">
      <div class="command-group-divider" />
      <h4 class="section-title">触发指令</h4>
      <p class="section-desc">一次性触发指令，不计入轮询调度，由用户手动操作或 API 触发</p>
      <div class="command-list">
        <div v-for="cmd in triggerCommands" :key="cmd.id" class="command-item trigger">
          <div class="cmd-info">
            <span class="cmd-name">{{ cmd.name }}</span>
            <el-tag size="small" type="danger">触发</el-tag>
            <span class="cmd-hex">0x{{ cmd.cmd_byte.toString(16).padStart(2, '0').toUpperCase() }}</span>
          </div>
          <div class="cmd-desc">{{ cmd.description }}</div>
          <div class="cmd-controls">
            <span class="trigger-hint">手动触发</span>
          </div>
        </div>
      </div>
    </template>
  </div>
  <div v-else-if="loaded" class="no-commands">
    <el-empty description="该设备类型不支持指令模板" :image-size="60" />
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
const triggerCommands = computed(() => commands.value.filter(c => !c.schedulable))

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
.command-item { display: flex; flex-wrap: nowrap; align-items: center; gap: 8px; padding: 8px; background: var(--el-bg-color); border-radius: 4px; overflow: hidden; min-width: 0; }
.command-item.disabled { opacity: 0.5; }
.command-item.trigger { border-left: 2px solid var(--el-color-danger); }
.cmd-info { display: flex; align-items: center; gap: 6px; flex: 1 1 40%; min-width: 0; }
.cmd-name { font-weight: 500; font-size: 13px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.cmd-hex { font-family: monospace; font-size: 11px; color: var(--el-text-color-secondary); flex-shrink: 0; }
.cmd-desc { flex: 1 1 30%; font-size: 11px; color: var(--el-text-color-secondary); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cmd-controls { display: flex; align-items: center; gap: 4px; flex: 1 1 60%; min-width: 0; justify-content: flex-end; }
.interval-unit { font-size: 11px; color: var(--el-text-color-secondary); }
.trigger-hint { font-size: 11px; color: var(--el-text-color-secondary); font-style: italic; }
.command-actions { margin-top: 12px; display: flex; align-items: center; gap: 8px; }
.saved-hint { font-size: 12px; color: var(--el-color-success); }
/* 轮询指令与触发指令两组之间的分区线，明确"保存"只属于轮询组 */
.command-group-divider { height: 1px; margin: 16px 0 12px; background: var(--el-border-color-lighter); }
.no-commands { margin-top: 16px; }
@media (max-width: 768px) {
  /* 移动端：每条指令一张带边框的小卡片，信息行 + 控制行上下排布 */
  .command-item {
    flex-wrap: wrap;
    align-items: center;
    padding: 10px 12px;
    border: 1px solid var(--el-border-color);
    border-radius: 8px;
  }
  /* 触发指令保留红色左强调边，与轮询指令的中性边框形成语义对照 */
  .command-item.trigger { border-left: 3px solid var(--el-color-danger); }
  .cmd-info {
    flex: 1 1 auto;
    width: auto;
  }
  .cmd-desc {
    flex: 1 1 100%;
    order: 3;
    white-space: normal;
    line-height: 1.4;
    margin-top: 6px;
  }
  .cmd-controls {
    flex: 0 0 auto;
    width: auto;
    justify-content: flex-end;
    gap: 6px;
  }
}
</style>
