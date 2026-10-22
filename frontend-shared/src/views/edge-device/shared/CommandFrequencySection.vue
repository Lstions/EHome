<template>
  <el-card
    v-if="deviceId"
    class="command-frequency-card"
    :class="{ 'embedded-card': embedded }"
    :shadow="embedded ? 'never' : 'hover'"
  >
    <template v-if="!embedded" #header>
      <div class="cf-head">
        <span class="cf-title">{{ title }}</span>
        <!-- 容量徽标：全站唯一口径 = 已启用条数 / 节点上报上限（无上限则不显示分母，不假装有上限） -->
        <el-tag
          v-if="showUsageBadge && limitValue > 0"
          :type="usageBadgeType"
          size="small"
          class="cf-badge"
        >已启用 {{ enabledCount }}/{{ limitValue }}</el-tag>
      </div>
    </template>

    <!--
      指令频率（轮询指令配置）—— **全站唯一实现**。
      2026-10-22 第七轮用户诉求③「指令频率应该抽一个公共组件出来，大家一起通用」：
        改前 BMS 详情页复制了一套 .poll-row + 自己的请求/保存/容量门（BmsDetailPage 里约 500 行），
        逆变器/通用设备页走 CommandFrequencySection + CommandList（另有 470 行）。
        两套实现从此各自漂移：BMS 有「超限点名 + 禁用按钮」，共享组件有「保存后确认节点已应用」，
        谁都不是完整答案，UI 还越长越不像。
        现在合并为**这一个组件**：上面两套的行为都收进来，三个设备页（BMS/逆变器/通用）全部用它。
      因此任何页面若再写一套私有轮询行，就是回退 —— 由 __tests__/CommandFrequencyShared.spec.ts 拦住。
    -->
    <div class="command-intervals" :class="{ embedded }">
      <!-- 首次加载：与内容结构相近的 skeleton -->
      <div v-if="!loaded && !loadFailed" class="loading-state" aria-busy="true">
        <el-skeleton :rows="3" animated />
      </div>

      <!-- 加载失败：显性错误 + 重试路径，不静默吞 -->
      <el-alert v-else-if="loadFailed" type="error" :closable="false" show-icon>
        <template #title>
          <div class="load-error-row">
            <span>轮询指令加载失败</span>
            <el-button size="small" type="primary" @click="loadCommands">重试</el-button>
          </div>
        </template>
      </el-alert>

      <template v-else-if="schedulableCommands.length > 0">
        <p v-if="noteText" class="section-desc">{{ noteText }}</p>

        <!--
          容量约束：把「保存后被 400 整单拒绝」提前成**保存前就能看见**的规则。
          改前共享组件只有一句「最多 N 条…」，BMS 页有点名禁用清单 —— 现在合并成两份都保留：
            · 超限：warning 告警 + 点名「该禁用哪几条」+ 行内「超限」标签 + 一键禁用按钮；
            · 占满：info 提示（再启用任何一条都会超限）。
          未上报上限（limitValue = 0）→ 一条容量提示都不出，不假装有上限。
        -->
        <el-alert v-if="overLimit" type="warning" :closable="false" show-icon class="capacity-alert">
          <template #title>
            已启用 {{ enabledCount }} 条，超过上限 {{ limitValue }} 条，保存会被拒绝
          </template>
          <div class="alert-detail">请先禁用 {{ enabledCount - limitValue }} 条：<b>{{ excessNames }}</b>。</div>
        </el-alert>
        <el-alert
          v-else-if="limitValue > 0 && enabledCount === limitValue"
          type="info"
          :closable="false"
          show-icon
          class="capacity-alert"
        >
          <template #title>已占满 {{ limitValue }}/{{ limitValue }} 条轮询额度</template>
          <div class="alert-detail">再启用任何一条都会超限；如需调整，请先禁用一条。</div>
        </el-alert>

        <div class="command-list">
          <!--
            行结构（容器感知，2026-10-22 第七轮修「指令频率内容被输入框覆盖遮挡」）：
              改前 ≤768px 用 grid-template-columns: minmax(0,1fr) auto —— 右列是 auto（输入框+ms+开关 ≈161px），
              左列 1fr 可以被压到 0。分屏/手机窄窗（≈320~360px）实测左列只剩 19px，
              而 .cmd-name 是 word-break:keep-all（不可断行）且没有 overflow-wrap ⇒ 命令名**画出格子**，
              被后画的输入框压住（真实页面 320px 下 11 行里 9 行重叠，DOM 重叠面积 659px²）。
              修法三层：
                ① 名称允许「实在放不下时」断行（keep-all + overflow-wrap:anywhere）⇒ 从「越界绘制」变成「换行」；
                ② 容器 <262px 时整行降级为单列（name / controls / desc 各占一行）⇒ 控件不再与文字争同一行；
                ③ 控件自身 flex-wrap + 输入框可收缩 ⇒ 任何宽度都不横向溢出。
              验证：__tests__/CommandFrequencyShared.spec.ts 的容器契约 + .logs/ui-fix/round7 多视口探针。
          -->
          <div
            v-for="cmd in schedulableCommands"
            :key="cmd.id"
            class="command-item"
            :class="{ disabled: !isEnabled(cmd.id), 'is-over': isExcess(cmd) }"
            :data-cmd-id="cmd.id"
          >
            <div class="cmd-identity">
              <span class="cmd-name">{{ cmd.name }}</span>
              <el-tag size="small" :type="cmd.type === 'read' ? 'info' : 'warning'">
                {{ cmd.type === 'read' ? '读' : '写' }}
              </el-tag>
              <!-- ASCII 协议（cmd_byte=0）显示 write_data 解码出的命令助记符，不显示假 0x00 -->
              <span v-if="commandToken(cmd)" class="cmd-hex" :title="`协议命令帧：${commandToken(cmd)}`">{{ commandToken(cmd) }}</span>
              <!-- 状态：文字 + 颜色双通道；超限/禁用优先表达「为什么不能保存」 -->
              <el-tag v-if="isExcess(cmd)" type="warning" size="small" effect="dark" class="cmd-state">超限</el-tag>
              <el-tag v-else-if="!isEnabled(cmd.id)" type="info" size="small" effect="plain" class="cmd-state">已禁用</el-tag>
              <el-tag v-else type="success" size="small" class="cmd-state">轮询中</el-tag>
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
              <el-button v-if="isExcess(cmd)" size="small" class="excess-disable" @click="onToggle(cmd.id, false)">禁用</el-button>
            </div>
          </div>
        </div>

        <!-- 保存失败：**常驻**在卡片内，不随 toast 消失 —— 拒绝原因必须留在页面上 -->
        <el-alert v-if="saveError" type="error" :closable="false" show-icon class="save-error">
          <template #title>保存失败，改动未生效（下方仍是节点当前配置）</template>
          <div class="save-error-detail">{{ saveError }}</div>
        </el-alert>

        <div class="command-actions">
          <el-button
            type="primary"
            size="small"
            :loading="saving"
            :disabled="!dirty || overLimit"
            @click="save"
          >保存</el-button>
          <span v-if="overLimit" class="hint is-warn">超限 {{ enabledCount }}/{{ limitValue }} 条，修正后才能保存</span>
          <span v-else-if="syncHint" class="saved-hint" :class="`is-${saveState}`">{{ syncHint }}</span>
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
  </el-card>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { edgeDeviceApi, type CommandTemplateWithInterval } from '@/api/edgeDevice'
import { nodeApi } from '@/api/node'
import { feedback, extractErrorMessage } from '@/utils/feedback'

type TagType = 'primary' | 'success' | 'warning' | 'info' | 'danger'

/**
 * 指令频率 / 轮询指令配置（共享组件，全站唯一实现）
 *
 * 消费方：
 *   · BMS 详情页（title="轮询指令配置"、需求上「上限由节点上报 + 超限点名」）
 *   · 逆变器详情页、通用设备详情页（title 默认「指令频率」）
 *   · dev 离线验证页（src/dev/MockBmsPanel.vue，embedded 形态）
 */
const props = withDefaults(defineProps<{
  deviceId: number
  /** 嵌入模式（外层已有卡片/折叠头）：去掉卡片外壳与灰底，减少一层嵌套 */
  embedded?: boolean
  /**
   * 所属节点的数值 id —— 保存后用它确认「配置是否真的被节点应用」。
   * 缺省时无法确认，提示退化为诚实的「已保存」（不再谎称「正在同步」）。
   */
  nodeId?: number | string
  /** 节点上报的同时可轮询指令条数上限（缺失/0 = 未上报，不显示任何容量提示） */
  maxPollingCommands?: number
  /** 卡片标题（BMS 页用「轮询指令配置」，其余页默认「指令频率」） */
  title?: string
  /** 说明文案；空字符串 = 不显示 */
  note?: string
  /** 卡头是否显示「已启用 x/y」容量徽标（未知上限时自动不显示） */
  showUsageBadge?: boolean
  /** 外部刷新令牌（如页头「刷新数据」）：变化时重新拉取指令列表 */
  reloadToken?: number
}>(), {
  embedded: false,
  nodeId: undefined,
  maxPollingCommands: undefined,
  title: '指令频率',
  note: '每条轮询指令都可独立设置轮询间隔（0 = 禁用）',
  showUsageBadge: true,
  reloadToken: 0,
})

/** 启用条数 / 上限的变化外抛，供消费页的页头摘要使用（避免页面再拉一份同一接口） */
const emit = defineEmits<{
  'usage-change': [payload: { enabled: number; limit: number; overLimit: boolean; dirty: boolean }]
}>()

const commands = ref<CommandTemplateWithInterval[]>([])
const localIntervals = reactive<Record<string, number>>({})
/** 加载/保存成功后的服务端基线，用于 dirty 判定（无改动时禁用保存并说明原因） */
const baselineIntervals = ref<Record<string, number>>({})
const saving = ref(false)
const loaded = ref(false)
const loadFailed = ref(false)
/**
 * 保存/同步状态：syncing（已保存，等节点确认）→ synced（节点已应用）
 * / unconfirmed（超时或拿不到 nodeId，诚实说「已保存」）。
 * 改前只有一个 saved 布尔且没有任何信号能复位它 ⇒ 提示永久停在「正在同步」。
 */
const saveState = ref<'idle' | 'syncing' | 'synced' | 'unconfirmed'>('idle')
/** 保存失败的原因，**常驻**渲染（拒绝原因必须留在页面上） */
const saveError = ref('')
/** 进入本次同步前的节点配置版本，用于判定「这次改动是否已被应用」 */
const nodeConfigVersion = ref('')
let operationGeneration = 0
let syncPollTimer: ReturnType<typeof setTimeout> | null = null

const noteText = computed(() => props.note)
/** 节点上报的上限（0 = 未上报/未知） */
const limitValue = computed(() => {
  const v = props.maxPollingCommands
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? Math.floor(v) : 0
})

const schedulableCommands = computed(() => commands.value.filter(c => c.schedulable))
/** 启用状态的唯一事实源 = 间隔数值（interval > 0 ⇔ 启用），不再有第二套 enabledMap */
const isEnabled = (cmdId: string): boolean => (localIntervals[cmdId] ?? 0) > 0
const enabledCount = computed(() => schedulableCommands.value.filter(c => isEnabled(c.id)).length)
const overLimit = computed(() => limitValue.value > 0 && enabledCount.value > limitValue.value)
/** 超限点名：与后端「整单拒绝」的口径一致，把超出上限的那几条明确标出来 */
const excessIds = computed(() => {
  if (!overLimit.value) return new Set<string>()
  return new Set(schedulableCommands.value.filter(c => isEnabled(c.id)).slice(limitValue.value).map(c => c.id))
})
const excessNames = computed(() =>
  schedulableCommands.value.filter(c => excessIds.value.has(c.id)).map(c => '「' + c.name + '」').join('、'))
function isExcess(cmd: { id: string }): boolean {
  return isEnabled(cmd.id) && excessIds.value.has(cmd.id)
}
const usageBadgeType = computed<TagType>(() => {
  if (limitValue.value === 0) return 'success'
  if (overLimit.value) return 'danger'
  return enabledCount.value === limitValue.value ? 'warning' : 'success'
})
/** 与节点基线存在差异时才允许保存 */
const dirty = computed(() => schedulableCommands.value.some(
  cmd => (localIntervals[cmd.id] ?? 0) !== (baselineIntervals.value[cmd.id] ?? 0),
))

/** 同步提示文案。三者都不得谎报状态：只有节点确认后才说「已同步到节点」 */
const syncHint = computed(() => {
  switch (saveState.value) {
    case 'syncing': return '已保存，正在同步到节点…'
    case 'synced': return '已同步到节点'
    case 'unconfirmed': return '已保存'
    default: return ''
  }
})

watch([enabledCount, limitValue, overLimit, dirty], () => {
  emit('usage-change', {
    enabled: enabledCount.value,
    limit: limitValue.value,
    overLimit: overLimit.value,
    dirty: dirty.value,
  })
}, { immediate: true })

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
 * 30s 上限：超时改为诚实的「已保存」，绝不无限「正在同步」。
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
      // 判定必须同时满足「版本已更新」：否则上一次遗留的 in_sync 会被误读成本次同步完成
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
    commands.value = Array.isArray(result) ? result : []
    const baseline: Record<string, number> = {}
    for (const cmd of commands.value) {
      if (cmd.schedulable) {
        localIntervals[cmd.id] = cmd.current_interval_ms
        baseline[cmd.id] = cmd.current_interval_ms
      }
    }
    baselineIntervals.value = baseline
    // 记录「本次改动前」的节点配置版本：保存后只有看到版本**变化**才判定同步完成
    nodeConfigVersion.value = await readNodeConfigVersion()
  } catch {
    if (generation !== operationGeneration || props.deviceId !== id) return
    commands.value = []
    loadFailed.value = true
  } finally {
    if (generation === operationGeneration && props.deviceId === id) loaded.value = true
  }
}

function hex(byte: number): string {
  return (byte & 0xff).toString(16).padStart(2, '0').toUpperCase()
}

/**
 * 指令标识徽标：不同协议族展示的东西不一样。
 *
 * 2026-10-20 生产实测（设备 15 泰琪丰逆变器）：11 条指令全部显示 0x00。
 * 根因不是前端读错字段，而是这类设备没有命令字节（inverter_techfine.go 对 ASCII
 * 行协议显式写 CmdByte: 0）。于是 0x00 成了既无信息又占位的假读数。
 * 修法：cmd_byte > 0 显示 0x03；cmd_byte == 0 从 write_data 解码 ASCII 助记符
 * （HSTS / HGRID / HOP…），解不出来则不显示，绝不回退成 0x00。
 */
function commandToken(cmd: { cmd_byte: number; write_data?: string }): string {
  if (cmd.cmd_byte > 0) return `0x${hex(cmd.cmd_byte)}`
  const raw = (cmd.write_data || '').replace(/[^0-9a-fA-F]/g, '')
  if (raw.length < 2 || raw.length % 2 !== 0) return ''
  let text = ''
  for (let i = 0; i < raw.length; i += 2) {
    const code = parseInt(raw.slice(i, i + 2), 16)
    // 只取可打印 ASCII；遇控制字符（\r / \n / NUL）即停止 —— 帧尾不是命令名的一部分
    if (code < 0x20 || code > 0x7e) break
    text += String.fromCharCode(code)
  }
  return text
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
  const enabled = value === true || value === 'true'
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
  // 超限时后端会整单拒绝（400 且不落库）：前端先把保存按钮判死，比「点了才报错」诚实
  if (overLimit.value) return
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
    // 改动前的节点版本基线；保存后版本变化才算本次同步完成
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
    // 拒绝原因必须**常驻页面**：toast 会消失，而「改完保存、刷新后没变化」正是因为它没被留住
    saveError.value = extractErrorMessage(e, '保存失败')
    feedback.handleError(e, '保存失败')
  } finally {
    // 无条件复位：设备切换/卸载后写入无害，若守卫内卡死会永久锁住新设备的保存按钮
    saving.value = false
  }
}

onMounted(loadCommands)
watch(() => props.deviceId, loadCommands)
watch(() => props.reloadToken, () => { void loadCommands() })
onUnmounted(() => { operationGeneration++; stopSyncPoll() })
</script>

<style scoped>
.cf-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.cf-title { font-size: 15px; font-weight: 600; color: var(--el-text-color-primary); }
.cf-badge { flex: 0 0 auto; }

.command-frequency-card { margin-top: 20px; }
.command-frequency-card.embedded-card { margin-top: 0; border: 0; box-shadow: none; }
.command-frequency-card.embedded-card :deep(.el-card__body) { padding: 4px 0 0; }

.command-intervals {
  padding: 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
  /* 容器查询的容器：下面 @container 规则按**内容宽**决定「两列 / 单列」，
     与视口宽度解耦（分屏、折叠面板内嵌、页面缩放都能正确降级）。 */
  container-type: inline-size;
}
/* 嵌入模式：去掉灰色面板底，指令行直接平铺，减少一层嵌套 */
.command-intervals.embedded { padding: 0; background: transparent; border-radius: 0; }
.section-desc { margin: 0 0 10px; font-size: 12px; line-height: 1.6; color: var(--el-text-color-secondary); }
.command-list { display: flex; flex-direction: column; gap: 8px; }

/*
  行布局：默认两列（名称 | 控件）+ 描述独占整行。
  · 右列 auto 承载「输入框 + ms + 开关」，左列 minmax(0,1fr) 允许收缩；
  · 左列收缩到放不下名称时，名称**断行**（keep-all + overflow-wrap:anywhere）而不是画出格子；
  · 容器（.command-intervals 内容宽）< 262px 时整行降级为单列（见文末 @container），
    控件不再与文字抢同一行 ⇒ 320px 分屏实测 0 重叠（改前 11 行里 9 行重叠）。
*/
.command-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-areas: 'name ctl' 'desc desc';
  align-items: center;
  gap: 6px 12px;
  padding: 8px 10px;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  min-width: 0;
}
.command-item.disabled { opacity: 0.6; }
.command-item.is-over { border-color: var(--el-color-warning-light-5); background: var(--el-color-warning-light-9); }

.cmd-identity { grid-area: name; display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; }
/*
  中文名称：keep-all 防「逐字竖排」（规范 §4.2.5）；overflow-wrap:anywhere 是
  「实在放不下时必须断行」的兜底 —— 少了它，名称会越出格子被右侧输入框压住（第七轮真机缺陷）。
*/
.cmd-name {
  font-weight: 600;
  font-size: 13px;
  color: var(--el-text-color-primary);
  word-break: keep-all;
  overflow-wrap: anywhere;
}
.cmd-hex { font-family: 'SF Mono', Consolas, Monaco, monospace; font-size: 11px; color: var(--el-text-color-secondary); }

.cmd-desc {
  grid-area: desc;
  min-width: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
  /*
    描述是**散文**，按正常中文断行即可（任意汉字间可断）。
    ⚠ 不能沿用标签的 word-break: keep-all：2026-10-22 实测 414px 下
    「PV2 电压、电流与功率（可选硬件；PV1-only 机型恒为 0）」整句被当成一个不可断的单位，
    行盒 289.72 > 内容宽 286，实际画出 3.72px（Chrome 里 keep-all 会压掉
    overflow-wrap:anywhere 的兜底机会）。规范 §4.2.5 要求 keep-all 的是**标签**，不是长段描述。
  */
  word-break: normal;
  overflow-wrap: anywhere;
}

.cmd-controls {
  grid-area: ctl;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
  min-width: 0;
}
.interval-input { width: 104px; flex: 0 1 104px; min-width: 84px; }
.interval-unit { font-size: 12px; color: var(--el-text-color-secondary); }
/* 开关触控区扩到 ≥44px（规范 §4.4.5） */
.enable-switch { padding: 10px 0; }

/* 容量告警 / 保存失败：常驻在卡片内（不随 toast 消失） */
.capacity-alert,
.save-error { margin: 0 0 10px; }
.alert-detail { margin-top: 4px; font-size: 12px; line-height: 1.6; word-break: break-word; }
.save-error-detail { margin-top: 4px; font-size: 12px; line-height: 1.6; word-break: break-word; }

.command-actions { margin-top: 12px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.saved-hint { font-size: 12px; color: var(--el-color-success); }
/* 等待节点确认期间用次级色：此时**还不能**断言成功，用成功色会误导 */
.saved-hint.is-syncing { color: var(--el-text-color-secondary); }
/* 拿不到确认（超时/未关联节点）也用次级色：只陈述「已保存」，不声称已应用 */
.saved-hint.is-unconfirmed { color: var(--el-text-color-secondary); }
.saved-hint.is-synced { color: var(--el-color-success); }
.synced-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.hint.is-warn { font-size: 12px; color: var(--el-color-warning); }
.loading-state { padding: 4px 0; }
.load-error-row { display: flex; align-items: center; gap: 12px; justify-content: space-between; }
.no-commands { margin-top: 8px; }

/* ---- 宽容器（≥769px 视口）：名称 / 描述 / 控件 单行排布（与改前桌面形态一致，描述单行省略） ---- */
@media (min-width: 769px) {
  .command-item {
    grid-template-columns: minmax(0, auto) minmax(0, 1fr) auto;
    grid-template-areas: 'name desc ctl';
    gap: 8px 12px;
  }
  /* 描述在桌面也换行显示：容器宽时自然一行，窄时折行 —— 历史实现用 nowrap+ellipsis，
     900px 实测 6 条描述被截断 19px（用户第七轮诉求②「内容显示不全」），且与移动端
     「整句可读」的契约不一致。整行高度由内容决定，不再有「看不见的字」。 */
  .cmd-desc { line-height: 1.45; }
  .interval-input { width: 130px; flex: 0 0 130px; }
}

/* ---- 窄屏（≤768px）：说明独占整行、「轮询中」标签与开关表达同一布尔值，隐藏省一行 ---- */
@media (max-width: 768px) {
  .command-intervals { padding: 10px; }
  .command-item { padding: 6px 8px; row-gap: 2px; }
  .cmd-state { display: none; }
  .cmd-desc { font-size: 11px; }
  .interval-input { width: 104px; }
  /*
    防 iOS 聚焦缩放（规范 §4.4.4：≤768px 输入控件字号必须 ≥16px）。
    Element Plus 的 size="small" 会把 .el-input__inner 钉在 12px（其特异性压过
    App.vue 里的全局 16px 规则，实测 2026-10-22 真机复现：点输入框整页放大）。
    这里在组件作用域内显式抬到 16px；输入框高度仍由 wrapper 的 44px 触控档决定。
  */
  .interval-input :deep(.el-input__inner) { font-size: 16px; }
}

/*
  容器降级（与视口无关）：内容宽 < 262px 时两列物理上放不下
  （控件列 ≈150px + 列间距 12px + 名称列最小 ≈84px），改单列堆叠 —— 这是
  「指令频率内容被覆盖遮挡」在 320~360px 分屏/窄窗下的根治手段。
*/
@container (max-width: 262px) {
  .command-item {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas: 'name' 'ctl' 'desc';
    row-gap: 4px;
  }
  /* 单列档：输入框 + ms 靠左成组，开关推到行尾（space-between 会把 ms 甩到中间，读起来像三列） */
  .cmd-controls { justify-content: flex-start; }
  .cmd-controls .enable-switch { margin-left: auto; }
  .interval-input { width: 96px; flex: 0 1 96px; }
}
</style>
