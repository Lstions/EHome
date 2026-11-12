<template>
  <div class="channel-terminal">
    <!-- 通道选择 + 控制栏 -->
    <div class="terminal-controls">
      <el-select
        v-model="selectedChannelId"
        placeholder="选择通道"
        size="small"
        style="width: 220px;"
        filterable
        @change="onChannelChange"
      >
        <el-option-group
          v-for="group in channelGroups"
          :key="group.type"
          :label="group.label"
        >
          <el-option
            v-for="ch in group.channels"
            :key="ch.id"
            :label="getChannelLabel(ch)"
            :value="ch.id ?? 0"
          />
        </el-option-group>
      </el-select>

      <el-tag v-if="selectedChannel" :type="getHardwareTagType(selectedChannel.hardware_type)" size="small">
        {{ getHardwareLabel(selectedChannel.hardware_type) }}
      </el-tag>

      <el-tag v-if="selectedChannel && selectedChannel.address" type="info" size="small">
        {{ selectedChannel.address }}
      </el-tag>

      <!-- 归一化后比较：后端 hardware_type 存**大写**（'UART'），小写字面量直接比较恒 false
           ⇒ 真实数据下波特率 tag 永不显示。同文件下方 showReadSize 同根因。 -->
      <template v-if="selectedChannel && String(selectedChannel.hardware_type || '').toLowerCase() === 'uart'">
        <el-tag type="info" size="small">{{ currentBaud }} baud</el-tag>
      </template>

      <div style="flex: 1;"></div>

      <el-radio-group v-model="displayMode" size="small">
        <el-radio-button value="hex">HEX</el-radio-button>
        <el-radio-button value="ascii">ASCII</el-radio-button>
      </el-radio-group>

      <!-- M3 修复：三个日志操作按钮收进一个**不换行的按钮组**。
           根因（390px 实测，可用宽 302px）：
             行需求 = radio 100 + gap8 + 暂停 71.8 + (gap8 + EP 的 .el-button + .el-button margin-left 12)
                      + 导出 65.7 + (gap8 + 12) + 清空 54 = **339.5px** > 302px ⇒ 溢出 37.5px。
           两个叠加原因：
             ① 间距翻倍：EP 的 `.el-button + .el-button { margin-left: 12px }` 叠在 flex `gap: 8px` 上，
                相邻按钮实际间距 20px（贡献 24px 冗余宽度）；
             ② 兜底仍不够：即便去掉全部 margin，四控件仍需 100+8+71.8+8+65.7+8+54 = 315.5px > 302px，
                因此**一行放不下四个**；而 flex 逐项换行时最后一项（清空）被挤成孤行。
           修法：按钮组整体换行 —— nowrap 保证「要么全在本行，要么整组一起下移」，
           不可能再出现单个按钮独占一行；组内间距显式补回 20px（8 gap + 12 margin），
           与改前实测间距完全一致（桌面与移动端本来都是 20px，实测按钮 x 坐标不变）。 -->
      <div class="terminal-actions">
        <el-button size="small" :aria-label="isPaused ? '继续终端日志' : '暂停终端日志'" @click="togglePause" :type="isPaused ? 'warning' : 'default'">
          {{ isPaused ? '▶ 继续' : '⏸ 暂停' }}
        </el-button>

        <el-button size="small" aria-label="导出终端日志" @click="exportLog" :disabled="logEntries.length === 0" title="导出日志">
          ↓ 导出
        </el-button>

        <el-button size="small" aria-label="清空终端日志" @click="clearLog" :disabled="logEntries.length === 0">
          清空
        </el-button>
      </div>
    </div>

    <!-- 双面板日志区域 -->
    <div class="terminal-dual-panel">
      <!-- 下行面板 (TX) -->
      <div class="terminal-panel tx-panel" :class="{ 'panel-paused': isPaused }">
        <div class="panel-header">
          <span class="panel-label">▼ 下行 (TX)</span>
          <span class="panel-count">{{ txEntries.length }}</span>
        </div>
        <div class="panel-log" ref="txLogContainer">
          <div v-if="txEntries.length === 0" class="panel-empty">无下行数据</div>
          <template v-for="(entry, index) in txEntries" :key="'tx-' + index">
            <div
              class="panel-entry"
              :class="[entry.type, { 'entry-expandable': getByteCount(entry.data) > 32 }]"
              @click="toggleExpand('tx', index)"
            >
              <span class="entry-dot dot-tx"></span>
              <span class="entry-time">{{ entry.time }}</span>
              <span class="entry-length">[{{ getByteCount(entry.data) }}B]</span>
              <span class="entry-data">{{ formatData(entry.data, 'tx', index) }}</span>
              <span v-if="getByteCount(entry.data) > 32 && !isExpanded('tx', index)" class="entry-ellipsis">...[点击展开]</span>
            </div>
          </template>
        </div>
      </div>

      <!-- 上行面板 (RX) -->
      <div class="terminal-panel rx-panel" :class="{ 'panel-paused': isPaused }">
        <div class="panel-header">
          <span class="panel-label">▲ 上行 (RX)</span>
          <span class="panel-count">{{ rxEntries.length }}</span>
        </div>
        <div class="panel-log" ref="rxLogContainer">
          <div v-if="rxEntries.length === 0" class="panel-empty">无上行数据</div>
          <template v-for="(entry, index) in rxEntries" :key="'rx-' + index">
            <div
              class="panel-entry"
              :class="[entry.type, { 'entry-expandable': getByteCount(entry.data) > 32 }]"
              @click="toggleExpand('rx', index)"
            >
              <span class="entry-dot" :class="entry.source === 'interactive' ? 'dot-interactive' : 'dot-rx'"></span>
              <span class="entry-time">{{ entry.time }}</span>
              <span class="entry-length">[{{ getByteCount(entry.data) }}B]</span>
              <span class="entry-data">{{ formatData(entry.data, 'rx', index) }}</span>
              <span v-if="getByteCount(entry.data) > 32 && !isExpanded('rx', index)" class="entry-ellipsis">...[点击展开]</span>
              <span v-if="entry.source === 'interactive'" class="entry-meta interactive">[交互]</span>
              <span v-if="entry.errorCode" class="entry-meta error">[ERR:{{ entry.errorCode }}]</span>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 发送区域 -->
    <ChannelCommandInput
      v-model:input-mode="inputMode"
      v-model:input-data="inputData"
      v-model:input-data-ascii="inputDataAscii"
      v-model:read-size="readSize"
      :show-read-size="showReadSize"
      :sending="sending"
      :disabled="!selectedChannelId"
      @send="sendData"
      @keydown="handleKeydown"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'
import { type Channel } from '@/api/channel'
// 发送区域是自包含模板片段，下沉为 ChannelCommandInput.vue：输入状态仍由本组件（会话组合式）
// 持有并 v-model 透传 —— 子组件不新增/不丢失任何状态，DOM/class/placeholder/v-if 分支与拆分前
// 逐字一致；模板里只保留原有的「发送区域」注释，不新增注释节点。
import ChannelCommandInput from './ChannelCommandInput.vue'
import { formatTerminalData, getByteCount } from './terminalCodec'
import { useChannelTerminalLogs } from './composables/useChannelTerminalLogs'
import { useChannelTerminalSession } from './composables/useChannelTerminalSession'

interface Props {
  collectorId: number | string
  nodeDeviceId?: string
  channels?: Channel[]
  initialChannelId?: number   // 挂载/变化时预选通道（self-fetch 与 channels prop 异步到达均只预选一次）
}

const props = defineProps<Props>()

// 两个日志面板容器：模板 ref 只能由组件持有；注入日志组合式用于「追加后滚底」。
const txLogContainer = ref<HTMLDivElement>()
const rxLogContainer = ref<HTMLDivElement>()

// 日志展示档（HEX/ASCII）是展示偏好，留在本组件；日志与会话状态各自下沉：
//   · 日志组合式：追加 / 1000 条上限 / 暂停 / 展开 / 导出（边界见该文件头）
//   · 会话组合式：通道清单 / 选中 / 代际隔离 / 发送 / 命令历史 / WS（边界见该文件头）
// 两个组合式都不认识对方的内部状态，靠 addLog / clearLog 两个回调接缝连接。
const displayMode = ref<'hex' | 'ascii'>('hex')
const {
  logEntries, txEntries, rxEntries, isPaused, togglePause, clearLog, addLog,
  isExpanded, toggleExpand, exportLog: exportLogEntries,
} = useChannelTerminalLogs({ txLogContainer, rxLogContainer })

const {
  selectedChannelId, selectedChannel, currentBaud, channelGroups, showReadSize,
  inputData, inputDataAscii, inputMode, readSize, sending,
  getChannelLabel, onChannelChange, sendData, handleKeydown,
} = useChannelTerminalSession(props, { addLog, clearLog })

/** 模板用：日志条目文本（展开态 + 展示档在本组件组合，纯格式化在 terminalCodec） */
const formatData = (data: string | any, panel: string, index: number) =>
  formatTerminalData(data, isExpanded(panel, index), displayMode.value)

/** 导出文件名要带当前通道 id（导出实现不依赖会话状态机，故在此注入） */
const exportLog = () => exportLogEntries(selectedChannelId.value)
</script>

<style scoped>
.channel-terminal { display: flex; flex-direction: column; gap: 12px; }
.terminal-controls { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
/* M3：日志操作按钮组 —— nowrap 使三按钮成为**不可分割的整体**，
   换行只发生在组这一级（整组下移），不会出现「清空」孤行。
   桌面间距必须与改前逐像素一致：改前按钮间距 = 外层 gap 8 + EP 的 `.el-button + .el-button` margin-left 12 = 20px。
   按钮组成为独立 flex 容器后，外层 gap 不再作用于组内 ⇒ 这里显式补回 `gap: 8px` + `margin-left: 12px`，
   合计仍是 20px（实测桌面改前后按钮 x 坐标一致，见任务报告）。 */
.terminal-actions { display: flex; align-items: center; flex-wrap: nowrap; flex-shrink: 0; gap: 8px; }
.terminal-actions .el-button + .el-button { margin-left: 12px; }
.terminal-dual-panel { display: flex; gap: 8px; height: 400px; }
.terminal-panel { flex: 1; min-width: 0; display: flex; flex-direction: column; border: 1px solid var(--el-border-color-lighter); border-radius: var(--radius-sm); overflow: hidden; }
.panel-header { display: flex; align-items: center; justify-content: space-between; padding: 6px 10px; border-bottom: 1px solid var(--el-border-color-lighter); font-size: 12px; font-weight: 500; }
.tx-panel .panel-header { background: var(--el-color-primary-light-9); color: var(--el-color-primary); }
.rx-panel .panel-header { background: var(--el-color-success-light-9); color: var(--el-color-success); }
.panel-paused .panel-header { animation: pause-blink 1.5s ease-in-out infinite; }
@keyframes pause-blink { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
.panel-label { color: inherit; }
.panel-count { font-size: 11px; opacity: 0.7; }
.panel-log { flex: 1; min-height: 0; overflow-y: auto; padding: 4px 0; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; font-size: 12px; line-height: 1.6; }
.panel-empty { color: var(--el-text-color-placeholder); text-align: center; padding: 20px; font-size: 12px; }
.panel-entry { display: flex; align-items: baseline; padding: 1px 8px; gap: 6px; min-height: 22px; min-width: 0; }
.panel-entry:hover { background: var(--el-fill-color-lighter); }
.panel-entry.error { color: var(--el-color-danger); }
.panel-entry.info { color: var(--el-color-success); }
.panel-entry.entry-expandable { cursor: pointer; }
.entry-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; margin-top: 6px; }
.dot-tx { background: var(--el-color-primary); }
.dot-rx { background: var(--el-color-success); }
.dot-interactive { background: var(--el-color-warning); }
.entry-time { color: var(--el-text-color-secondary); font-size: 11px; flex-shrink: 0; }
.entry-length { color: var(--el-text-color-secondary); font-size: 10px; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; flex-shrink: 0; opacity: 0.7; }
.entry-data { min-width: 0; white-space: pre-wrap; word-break: break-all; }
.entry-ellipsis { color: var(--el-color-primary); font-size: 10px; cursor: pointer; flex-shrink: 0; }
.entry-meta { font-size: 10px; opacity: 0.8; flex-shrink: 0; }
.entry-meta.interactive { color: var(--el-color-warning); }
.entry-meta.error { color: var(--el-color-danger); }
/* 发送卡（模式切换 / 两个输入框 / RX 字节数 / 发送按钮）的 scoped 规则随模板片段
   搬到 ChannelCommandInput.vue：scoped 样式不跨组件边界生效，跟着片段走才不会出现孤儿选择器。 */

@media (max-width: 768px) {
  .terminal-controls > :first-child {
    width: 100% !important;
  }

  .terminal-controls > div[style*="flex"] {
    display: none;
  }

  .terminal-dual-panel {
    flex-direction: column;
    height: auto;
  }

  .terminal-panel {
    height: 260px;
    flex: none;
  }
}
</style>
