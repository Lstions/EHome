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
            <!--
              状态：文字 + 颜色双通道（规范 4.2.1/4.2.2）；禁用用 info 灰，不用 danger。

              ⚠ 2026-10-20 修（用户报障「逆变器指令配置的指令显示不对」）：
              这里原先是「轮询中 · 10000 ms」——但下方输入框右侧**已经**有独立的
              「ms」单位，于是同一行出现两次 ms，读屏/复制文本也读成两个单位。
              单位只保留一处（输入框旁），状态标签只表达「是否在轮询」，
              与 BMS 页同类组件（BmsDetailPage 的 poll-row）保持同一形态。
            -->
            <el-tag size="small" :type="isEnabled(cmd.id) ? 'success' : 'info'" class="cmd-state">
              {{ isEnabled(cmd.id) ? '轮询中' : '已禁用' }}
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
      <!-- 容量约束（2026-10-15 报障修复）：把"保存后被 400 整单拒绝"提前为可见规则。
           后端 ValidateManifestCommandCapacity 在启用条数 > 上限时返回 400 且**不落库**，
           改前只在 toast 闪一下 → 用户刷新看到旧值，以为保存无效。 -->
      <el-alert
        v-if="maxPollingCommands && enabledCount > maxPollingCommands"
        type="warning"
        :closable="false"
        show-icon
        class="capacity-alert"
      >
        <template #title>
          最多 {{ maxPollingCommands }} 条指令可同时轮询，当前已启用 {{ enabledCount }} 条；
          请先将其余 {{ enabledCount - maxPollingCommands }} 条设为 0（禁用）
        </template>
      </el-alert>
      <!-- 保存失败：**常驻**在卡片内，不随 toast 消失。
           这是"改完保存、刷新后没变化"能被解释的唯一途径——拒绝原因必须留在页面上。 -->
      <el-alert v-if="saveError" type="error" :closable="false" show-icon class="save-error">
        <template #title>保存失败，改动未生效（下方仍是节点当前配置）</template>
        <div class="save-error-detail">{{ saveError }}</div>
      </el-alert>
      <div class="command-actions">
        <el-button type="primary" size="small" :loading="saving" :disabled="!dirty" @click="save">保存</el-button>
        <span v-if="syncHint" class="saved-hint" :class="`is-${saveState}`">{{ syncHint }}</span>
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
import { nodeApi } from '@/api/node'
import { feedback, extractErrorMessage } from '@/utils/feedback'

const props = defineProps<{
  deviceId: number
  /** 嵌入模式（移动端折叠面板内）：去掉灰色面板底，减少嵌套层级 */
  embedded?: boolean
  /**
   * 所属节点的数值 id —— 用于在保存后确认"配置是否真的被节点应用"。
   * 缺省时无法确认，提示退化为诚实的「已保存」（不再谎称"正在同步"）。
   */
  nodeId?: number | string
  /** 后端 MaxCommandsPerEdgeDevice：同时可轮询的指令条数上限（随设备详情下发） */
  maxPollingCommands?: number
}>()

const commands = ref<CommandTemplateWithInterval[]>([])
const localIntervals = reactive<Record<string, number>>({})
/** 加载/保存成功后的服务端基线，用于 dirty 判定（无改动时禁用保存并说明原因） */
const baselineIntervals = ref<Record<string, number>>({})
const saving = ref(false)
const loaded = ref(false)
const loadFailed = ref(false)
/**
 * 保存/同步状态（2026-10-15 报障修复）。
 *
 * 改前只有一个 `saved` 布尔：保存成功后置 true，**此后没有任何信号能把它复位**
 * （只有再次编辑或重挂载才清），于是提示永久停在「已保存，正在同步到节点...」。
 * 而实测后端在 ~300ms 内就完成了推送并收到 ConfigResult success=true ——
 * 也就是说同步早就完成了，界面却在永远"正在同步"（用户报障 #2）。
 *
 * 现在按真实链路分档：syncing（已保存，等节点确认）→ synced（节点已应用）
 * / unconfirmed（超时或拿不到 nodeId，诚实说"已保存"）。
 */
const saveState = ref<'idle' | 'syncing' | 'synced' | 'unconfirmed'>('idle')
/** 保存失败的原因，**常驻**渲染（用户报障 #1：拒绝原因必须留在页面上） */
const saveError = ref('')
/** 进入本次同步前的节点配置版本，用于判定"这次改动是否已被应用" */
const nodeConfigVersion = ref('')
let operationGeneration = 0
let syncPollTimer: ReturnType<typeof setTimeout> | null = null

/** 同步提示文案。三者都不得谎报状态：只有节点确认后才说"已同步到节点"。 */
const syncHint = computed(() => {
  switch (saveState.value) {
    case 'syncing': return '已保存，正在同步到节点…'
    case 'synced': return '已同步到节点'
    case 'unconfirmed': return '已保存'
    default: return ''
  }
})

/** 已启用（间隔 > 0）的指令条数 —— 与后端容量门的口径一致 */
const enabledCount = computed(() => schedulableCommands.value.filter(c => isEnabled(c.id)).length)

function stopSyncPoll() {
  if (syncPollTimer) { clearTimeout(syncPollTimer); syncPollTimer = null }
}

/** 读取节点当前配置版本（失败静默：确认只是增强，不阻断保存） */
async function readNodeConfigVersion(): Promise<string> {
  if (props.nodeId === undefined || props.nodeId === '') return ''
  try {
    const node = await nodeApi.getDetail(props.nodeId as number | string)
    return node?.config_version ?? ''
  } catch {
    return ''
  }
}

/**
 * 保存后轮询节点，直到它报告已应用**新**版本（config_version 变化 + in_sync）。
 * 30s 上限：超时改为诚实的「已保存」，绝不无限"正在同步"。
 */
async function confirmSync(generation: number, baselineVersion: string) {
  if (props.nodeId === undefined || props.nodeId === '') {
    saveState.value = 'unconfirmed'
    return
  }
  const deadline = Date.now() + 30000
  const tick = async () => {
    if (generation !== operationGeneration) return
    try {
      const node = await nodeApi.getDetail(props.nodeId as number | string)
      if (generation !== operationGeneration) return
      const version = node?.config_version ?? ''
      const inSync = node?.config_sync_state === 'in_sync'
      // 判定必须同时满足"版本已更新"：否则上一次遗留的 in_sync 会被误读成本次同步完成。
      if (inSync && version && version !== baselineVersion) {
        saveState.value = 'synced'
        nodeConfigVersion.value = version
        return
      }
      if (node?.config_sync_state === 'failed') {
        saveState.value = 'unconfirmed'
        return
      }
    } catch {
      // 读取失败按未确认继续轮询，超时后收敛为「已保存」
    }
    if (Date.now() >= deadline) { saveState.value = 'unconfirmed'; return }
    syncPollTimer = setTimeout(tick, 1000)
  }
  stopSyncPoll()
  syncPollTimer = setTimeout(tick, 600)
}

async function loadCommands() {
  const id = props.deviceId
  const generation = ++operationGeneration
  loaded.value = false
  loadFailed.value = false
  // 切换设备/重载时重置保存/同步提示，避免上一台设备的状态残留到新列表
  saveState.value = 'idle'
  saveError.value = ''
  stopSyncPoll()
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
    // 记录"本次改动前"的节点配置版本：保存后只有看到版本**变化**才判定同步完成
    // （否则上一次遗留的 in_sync 会被误读成本次已应用）。读取失败保持空串，退化为超时路径。
    nodeConfigVersion.value = await readNodeConfigVersion()
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
  // 再次编辑即说明上一次的保存/同步结论已过期，收起提示（同时清掉失败原因）
  saveState.value = 'idle'
  saveError.value = ''
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
  saveState.value = 'idle'
  saveError.value = ''
}

async function save() {
  const id = props.deviceId
  const generation = operationGeneration
  saving.value = true
  saveState.value = 'idle'
  saveError.value = ''
  try {
    const intervals: Record<string, number> = {}
    for (const cmd of schedulableCommands.value) {
      intervals[cmd.id] = localIntervals[cmd.id] ?? 0
    }
    // 改动前的节点版本基线；保存后版本变化才算本次同步完成。
    const baselineVersion = nodeConfigVersion.value || await readNodeConfigVersion()
    if (generation !== operationGeneration || props.deviceId !== id) return
    await edgeDeviceApi.updateCommandIntervals(id, intervals)
    if (generation !== operationGeneration || props.deviceId !== id) return
    baselineIntervals.value = { ...intervals }
    saveState.value = 'syncing'
    feedback.success('指令频率已保存，正在同步到节点')
    void confirmSync(generation, baselineVersion)
  } catch (e: unknown) {
    if (generation !== operationGeneration || props.deviceId !== id) return
    // ⚠ 拒绝原因必须**常驻页面**：toast 会消失，而"改完保存、刷新后没变化"
    // 正是因为它没被留住（容量门 400 / 校验 400 都走这里）。
    saveError.value = extractErrorMessage(e, '保存失败')
    feedback.handleError(e, '保存失败')
  } finally {
    // 无条件复位：设备切换/卸载后写入无害，若守卫内卡死会永久锁住新设备的保存按钮（规范 3.3.2/3.3.4）
    saving.value = false
  }
}

onMounted(loadCommands)
watch(() => props.deviceId, loadCommands)
onUnmounted(() => { operationGeneration++; stopSyncPoll() })
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
/* 容量告警 / 保存失败：常驻在卡片内（不随 toast 消失）。
   2026-10-15：改前失败只在 toast 里闪一下，用户刷新后看到旧值→以为"保存无效"。
   现在拒绝原因留在页面上，且与上方告警之间留出间距。 */
.capacity-alert,
.save-error { margin-top: 12px; }
.save-error-detail {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.6;
  word-break: break-word;
}
.command-actions { margin-top: 12px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.saved-hint { font-size: 12px; color: var(--el-color-success); }
/* 等待节点确认期间用次级色：此时**还不能**断言成功，用成功色会误导 */
.saved-hint.is-syncing { color: var(--el-text-color-secondary); }
/* 拿不到确认（超时/未关联节点）也用次级色：只陈述"已保存"，不声称已应用 */
.saved-hint.is-unconfirmed { color: var(--el-text-color-secondary); }
.saved-hint.is-synced { color: var(--el-color-success); }
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
