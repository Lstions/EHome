<template>
  <div class="command-intervals" :class="{ embedded }">
    <!-- 首次加载：与内容结构相近的 skeleton（规范 4.3 状态 2） -->
    <div v-if="!loaded && !loadFailed" class="loading-state" aria-busy="true">
      <el-skeleton :rows="3" animated />
    </div>

    <!-- 加载失败：显性错误 + 重试路径，不静默吞（规范 3.4.6） -->
    <el-alert v-else-if="loadFailed" type="error" :closable="false" show-icon>
      <template #title>
        <div class="load-error-row">
          <span>轮询指令加载失败</span>
          <el-button size="small" type="primary" @click="loadCommands">重试</el-button>
        </div>
      </template>
    </el-alert>

    <template v-else-if="schedulableCommands.length > 0">
      <h4 class="section-title">轮询指令</h4>
      <p class="section-desc">每条指令可独立设置轮询间隔（0 = 禁用）</p>
      <div class="command-list">
        <div
          v-for="cmd in schedulableCommands"
          :key="cmd.id"
          class="command-item"
          :class="{ disabled: !isEnabled(cmd.id) }"
        >
          <div class="cmd-identity">
            <span class="cmd-name">{{ cmd.name }}</span>
            <el-tag size="small" :type="cmd.type === 'read' ? 'info' : 'warning'">
              {{ cmd.type === 'read' ? '读' : '写' }}
            </el-tag>
            <span class="cmd-hex">0x{{ hex(cmd.cmd_byte) }}</span>
            <!-- 状态：文字 + 颜色双通道（规范 4.2.1/4.2.2）；禁用用 info 灰，不用 danger -->
            <el-tag size="small" :type="isEnabled(cmd.id) ? 'success' : 'info'" class="cmd-state">
              {{ isEnabled(cmd.id) ? `轮询中 · ${localIntervals[cmd.id]} ms` : '已禁用' }}
            </el-tag>
          </div>
          <div v-if="cmd.description" class="cmd-desc" :title="cmd.description">{{ cmd.description }}</div>
          <div class="cmd-controls">
            <el-input-number
              v-model="localIntervals[cmd.id]"
              :min="0"
              :max="60000"
              :step="1000"
              size="small"
              controls-position="right"
              class="interval-input"
              :aria-label="`${cmd.name} 轮询间隔（毫秒，0 = 禁用）`"
              @change="(value: number | undefined) => onIntervalInput(cmd.id, value)"
            />
            <span class="interval-unit">ms</span>
            <el-switch
              :model-value="isEnabled(cmd.id)"
              size="small"
              class="enable-switch"
              :aria-label="`${cmd.name} 启用轮询`"
              @change="(value: string | number | boolean) => onToggle(cmd.id, value)"
            />
          </div>
        </div>
      </div>
      <div class="command-actions">
        <el-button type="primary" size="small" :loading="saving" :disabled="!dirty" @click="save">保存</el-button>
        <span v-if="saved" class="saved-hint">已保存，正在同步到节点...</span>
        <span v-else-if="!dirty" class="synced-hint">当前配置与节点一致</span>
      </div>
    </template>

    <div v-else class="no-commands">
      <EmptyState
        kind="empty"
        size="small"
        title="无可配置的轮询指令"
        description="该设备没有可配置的轮询指令；一次性读取请使用下方受控操作"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { edgeDeviceApi, type CommandTemplateWithInterval } from '@/api/edgeDevice'
import { feedback } from '@/utils/feedback'

const props = defineProps<{
  deviceId: number
  /** 嵌入模式（移动端折叠面板内）：去掉灰色面板底，减少嵌套层级 */
  embedded?: boolean
}>()

const commands = ref<CommandTemplateWithInterval[]>([])
const localIntervals = reactive<Record<string, number>>({})
/** 加载/保存成功后的服务端基线，用于 dirty 判定（无改动时禁用保存并说明原因） */
const baselineIntervals = ref<Record<string, number>>({})
const saving = ref(false)
const saved = ref(false)
const loaded = ref(false)
const loadFailed = ref(false)
let operationGeneration = 0

async function loadCommands() {
  const id = props.deviceId
  const generation = ++operationGeneration
  loaded.value = false
  loadFailed.value = false
  // 切换设备/重载时重置保存提示，避免上一台设备的「已保存」残留到新列表
  saved.value = false
  try {
    const result = await edgeDeviceApi.getCommandIntervals(id)
    if (generation !== operationGeneration || props.deviceId !== id) return
    commands.value = result
    const baseline: Record<string, number> = {}
    for (const cmd of commands.value) {
      if (cmd.schedulable) {
        localIntervals[cmd.id] = cmd.current_interval_ms
        baseline[cmd.id] = cmd.current_interval_ms
      }
    }
    baselineIntervals.value = baseline
  } catch {
    if (generation !== operationGeneration || props.deviceId !== id) return
    commands.value = []
    loadFailed.value = true
  } finally {
    if (generation === operationGeneration && props.deviceId === id) loaded.value = true
  }
}

const schedulableCommands = computed(() => commands.value.filter(c => c.schedulable))

/** 启用状态的唯一事实源 = 间隔数值（interval > 0 ⇔ 启用），不再有第二套 enabledMap */
const isEnabled = (cmdId: string): boolean => (localIntervals[cmdId] ?? 0) > 0

/** 与节点基线存在差异时才允许保存 */
const dirty = computed(() => schedulableCommands.value.some(
  cmd => (localIntervals[cmd.id] ?? 0) !== (baselineIntervals.value[cmd.id] ?? 0),
))

function hex(byte: number): string {
  return (byte & 0xff).toString(16).padStart(2, '0').toUpperCase()
}

/** 手动输入间隔：归一化越界/非法值；interval=0 即禁用语义由 isEnabled 派生。
 * 清空输入（undefined）归一为 0（禁用）：fail-closed，状态 tag 立即可见 */
function onIntervalInput(cmdId: string, value: number | undefined) {
  const normalized = Number.isFinite(value) && (value as number) >= 0
    ? Math.min(Math.floor(value as number), 60000)
    : 0
  localIntervals[cmdId] = normalized
  saved.value = false
}

/** 开关：ON 且间隔为 0 时恢复模板默认间隔；OFF 置 0（禁用） */
function onToggle(cmdId: string, value: string | number | boolean) {
  const enabled = value === true
  if (enabled) {
    if (!localIntervals[cmdId] || localIntervals[cmdId] <= 0) {
      const cmd = commands.value.find(c => c.id === cmdId)
      localIntervals[cmdId] = (cmd?.interval_ms && cmd.interval_ms > 0) ? cmd.interval_ms : 5000
    }
  } else {
    localIntervals[cmdId] = 0
  }
  saved.value = false
}

async function save() {
  const id = props.deviceId
  const generation = operationGeneration
  saving.value = true
  saved.value = false
  try {
    const intervals: Record<string, number> = {}
    for (const cmd of schedulableCommands.value) {
      intervals[cmd.id] = localIntervals[cmd.id] ?? 0
    }
    await edgeDeviceApi.updateCommandIntervals(id, intervals)
    if (generation !== operationGeneration || props.deviceId !== id) return
    baselineIntervals.value = { ...intervals }
    saved.value = true
    feedback.success('指令频率已保存，正在同步到节点')
  } catch (e: unknown) {
    if (generation !== operationGeneration || props.deviceId !== id) return
    feedback.handleError(e, '保存失败')
  } finally {
    // 无条件复位：设备切换/卸载后写入无害，若守卫内卡死会永久锁住新设备的保存按钮（规范 3.3.2/3.3.4）
    saving.value = false
  }
}

onMounted(loadCommands)
watch(() => props.deviceId, loadCommands)
onUnmounted(() => { operationGeneration++ })
</script>

<style scoped>
.command-intervals { margin-top: 16px; padding: 12px; background: var(--el-fill-color-lighter); border-radius: 8px; }
/* 嵌入模式：去掉灰色面板底，指令行直接平铺，减少一层嵌套 */
.command-intervals.embedded { margin-top: 0; padding: 0; background: transparent; border-radius: 0; }
.section-title { margin: 0 0 4px; font-size: 14px; font-weight: 600; color: var(--el-text-color-primary); }
.section-desc { margin: 0 0 12px; font-size: 12px; color: var(--el-text-color-secondary); }
.command-list { display: flex; flex-direction: column; gap: 8px; }
/* 容器感知契约：flex-wrap 降级，禁止固定宽度 + nowrap 裁剪（390px 下 180+200>310 必然溢出） */
.command-item {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  padding: 8px 10px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
}
.command-item.disabled { opacity: 0.6; }
.cmd-identity { display: flex; align-items: center; gap: 6px; min-width: 0; flex-wrap: wrap; }
.cmd-name { font-weight: 600; font-size: 13px; color: var(--el-text-color-primary); word-break: keep-all; }
.cmd-hex { font-family: 'SF Mono', Consolas, Monaco, monospace; font-size: 11px; color: var(--el-text-color-secondary); }
.cmd-state { margin-left: 2px; }
.cmd-desc {
  flex: 1 1 160px;
  min-width: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cmd-controls { display: flex; align-items: center; gap: 8px; margin-left: auto; }
.interval-input { width: 130px; }
.interval-unit { font-size: 12px; color: var(--el-text-color-secondary); }
.command-actions { margin-top: 12px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.saved-hint { font-size: 12px; color: var(--el-color-success); }
.synced-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.loading-state { padding: 4px 0; }
.load-error-row { display: flex; align-items: center; gap: 12px; justify-content: space-between; }
.no-commands { margin-top: 8px; }

/* ≤768px：两段式纵向堆叠，控件行占满宽 —— 窄容器下裁剪从结构上不可能 */
@media (max-width: 768px) {
  .command-item { flex-direction: column; align-items: stretch; gap: 8px; }
  /* 桌面 row 方向的 flex-basis:160px 作用在宽度上；column 后会错误作用到高度，
     撑出 ~160px 空白 —— 移动端必须重置为内容高度 */
  .cmd-desc { flex: none; white-space: normal; word-break: keep-all; }
  .cmd-controls { margin-left: 0; justify-content: space-between; min-height: 44px; }
  .interval-input { flex: 1; width: auto; }
  /* 输入框字号 ≥16px 防 iOS 聚焦缩放，由 App.vue 全局媒体查询统一保证 */
  /* 开关触控区扩到 ≥44px（规范 4.4.5） */
  .enable-switch { padding: 11px 0; }
}
</style>
