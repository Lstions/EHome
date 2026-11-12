<template>
        <section class="card device-card">
          <div class="card-head">
            <span class="card-title">关联设备<span v-if="devices.length > 0" class="device-count">（共 {{ devices.length }}）</span></span>
            <span class="device-card-actions">
              <span class="view-switch" role="tablist" aria-label="视图切换">
                <button type="button" class="view-switch-btn" :class="{ active: deviceViewMode === 'list' }" @click="deviceViewMode = 'list'">列表</button>
                <button type="button" class="view-switch-btn" :class="{ active: deviceViewMode === 'card' }" @click="deviceViewMode = 'card'">卡片</button>
              </span>
              <button class="btn btn-primary btn-sm" :disabled="!nodeId || nodeOffline" @click="createDevice"><el-icon :size="13"><Plus /></el-icon>创建设备</button>
              <button class="btn btn-plain btn-sm" :disabled="devicesLoading" @click="refreshDevices"><el-icon :size="13" :class="{ spin: devicesLoading }"><RefreshRight /></el-icon>{{ devicesLoading ? '加载中…' : '刷新' }}</button>
            </span>
          </div>
          <div v-if="devicesLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <div v-else-if="devices.length === 0" class="card-empty">暂无设备</div>
          <!-- 列表视图 -->
          <div v-else-if="deviceViewMode === 'list'" class="chan-list">
            <div v-for="row in devices" :key="row.id" class="chan-row device-row">
              <span class="chan-icon"><el-icon :size="14"><Connection /></el-icon></span>
              <span class="chan-name">{{ row.name }}</span>
              <span class="chan-sub">{{ getDeviceTypeLabel(row.device_type) }}</span>
              <span class="chan-sub mono">{{ String(row.hardware_id || '') }}</span>
              <span class="chan-sub mono">{{ deviceChannelText(row) }}</span>
              <span class="chan-sub device-last-data" :title="formatLastData(row.last_data)">{{ formatLastData(row.last_data) }}</span>
              <StatusBadge :status="row.status" />
              <span class="chan-sub device-last-time">{{ row.last_data_time ? formatTime(row.last_data_time) : '—' }}</span>
              <button class="link-btn" type="button" @click="viewDevice(row)"><el-icon :size="12"><View /></el-icon>查看</button>
            </div>
          </div>
          <!-- 卡片视图（设计稿 new-node-edge.png 卡片模式） -->
          <div v-else class="device-grid">
            <!--
              is-offline 只对**真正离线/禁用**的设备生效（2026-10-03 审查发现）。
              原判据 `row.status !== 'online'` 会把 pending（已创建、还没采到数据）
              也变暗（CSS 里 is-offline 是 opacity .72），而同一行的 StatusBadge 却
              渲染 warning 色的「等待中」—— 一行里两个相反信号。
              与 StatusBadge 同口径：只有 offline/disabled/error 才算"非正常在线"。
            -->
            <div
              v-for="row in devices"
              :key="row.id"
              class="device-tile"
              :class="{ 'is-offline': isDeviceOffline(row.status) }"
              @click="viewDevice(row)"
            >
              <div class="device-tile-head">
                <span class="device-tile-icon"><el-icon :size="18"><Connection /></el-icon></span>
                <div class="device-tile-title">
                  <b class="device-tile-name" :title="row.name">{{ row.name }}</b>
                  <span class="device-tile-type">{{ getDeviceTypeLabel(row.device_type) }}</span>
                </div>
                <StatusBadge :status="row.status" />
              </div>
              <div class="device-tile-body">
                <div class="device-tile-row"><span>总线地址</span><b class="mono">{{ String(row.hardware_id || '—') }}</b></div>
                <div class="device-tile-row"><span>所在通道</span><b class="mono">{{ deviceChannelText(row) }}</b></div>
              </div>
              <div class="device-tile-reading" :class="{ 'no-data': !row.last_data }">
                <template v-if="row.last_data">
                  <span class="reading-label">最新读数</span>
                  <strong class="reading-value" :title="formatLastData(row.last_data)">{{ formatLastData(row.last_data) }}</strong>
                  <span class="reading-time">{{ row.last_data_time ? formatTime(row.last_data_time) : '—' }}</span>
                </template>
                <template v-else><span class="reading-none">等待首条采集数据</span></template>
              </div>
            </div>
          </div>
        </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { Connection, Plus, RefreshRight, View } from '@element-plus/icons-vue'
import type { Channel } from '@/api/channel'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { formatTime } from '@/utils/format'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { channelHardwareDisplay } from '../composables/channelDisplay'

/**
 * 「关联设备」页签：列表/卡片两种视图 + 读数预览。
 *
 * 为什么单独成组件：设备的"离线变暗"判据与 StatusBadge 的语义必须同口径，
 * 而这条判据历史上出过两次偏差（把 pending 当离线、把 internal 键当通道名）。
 * 集中在一个文件里，改显示口径时不会漏掉另一处。
 *
 * 视图模式是纯展示态（不跨页签共享），因此留在组件内；创建设备对话框需要
 * 节点身份与通道列表，属于页面级资源，故以事件回抛。
 */
const props = defineProps<{
  devices: any[]
  devicesLoading: boolean
  nodeOffline: boolean
  /** 节点序列号；空串时"创建设备"必须禁用（后端没有可归属的节点）。 */
  nodeId?: string
  channels: Channel[]
}>()

const emit = defineEmits<{
  'view-device': [row: any]
  'refresh-devices': []
  'create-device': []
}>()

const deviceViewMode = ref<'list' | 'card'>('list')

// isDeviceOffline 决定设备卡是否显示"变暗"（.is-offline → opacity .72）。
//
// 2026-10-03 审查发现：原判据 `status !== 'online'` 把 pending 也算成离线，
// 而同一行的 StatusBadge 对 pending 渲染 warning 色的「等待中」——
// 一行之内给出两个相反信号。pending 的语义是"已创建、还在等第一帧数据"，
// 既不是正常在线，也不是故障，不该被当作离线变暗。
// 与 StatusBadge 的 warning/danger 口径保持一致：offline/disabled/error 才算。
function isDeviceOffline(status: unknown): boolean {
  const s = String(status ?? '').toLowerCase()
  return s === 'offline' || s === 'disabled' || s === 'error'
}

function deviceChannelText(row: any): string {
  const channel = props.channels.find(ch => ch.id === row.channel_id)
  if (!channel) return '—'
  // 与 channelName 同一口径：hardware_id 已带总线名时不再重复前缀（旧写法出 "I2C I2C0"）。
  return channelHardwareDisplay(channel) || '—'
}

function formatLastData(data: Record<string, any> | null): string {
  if (!data) return '—'
  const entries = Object.entries(data).filter(([k]) => k !== 'error_code' && k !== 'raw_data')
  if (entries.length === 0) return '—'
  return entries.slice(0, 3).map(([k, v]) => {
    const unit = sensorUnitMap[k] || ''
    const name = sensorNameMap[k] || k
    return `${name}: ${typeof v === 'number' ? v.toFixed(v < 10 ? 2 : 0) : v}${unit ? unit : ''}`
  }).join('  ')
}

function viewDevice(row: any) { emit('view-device', row) }
function refreshDevices() { emit('refresh-devices') }
function createDevice() { emit('create-device') }
</script>

<style scoped>
/* 关联设备卡的桌面规则；移动端设备行规则在 nodeOverview.shared.css 的唯一 768 块里。 */
/* 关联设备 */
.device-card .chan-sub { font-size: 12px; color: var(--no-text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.device-row { gap: 10px; }
.device-row .device-last-data { flex: 1 1 18%; min-width: 0; color: var(--no-text-secondary); }
.device-row .device-last-time { flex: 0 0 auto; color: var(--no-text-muted); }
.device-row .link-btn { flex: 0 0 auto; white-space: nowrap; }
.device-count { font-size: 13px; font-weight: 400; color: var(--no-text-muted); }

/* 列表/卡片切换（设计稿 segmented 文字切换） */
.view-switch { display: inline-flex; align-items: center; background: var(--no-chip-off-bg); border-radius: 6px; padding: 2px; gap: 2px; }
.view-switch-btn {
  height: 26px; padding: 0 14px; border: 0; border-radius: 5px; font-size: 12px; cursor: pointer;
  background: transparent; color: var(--no-text-secondary); font-family: inherit; transition: all .15s;
}
.view-switch-btn.active { background: var(--no-primary); color: var(--text-on-fill); font-weight: 500; }
.view-switch-btn:not(.active):hover { color: var(--no-primary); }

/* 卡片视图网格 */
.device-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.device-tile {
  min-width: 0; border: 1px solid var(--no-border-light); border-radius: 8px; padding: 14px 16px;
  display: flex; flex-direction: column; gap: 10px; background: var(--card-bg, #fff);
  cursor: pointer; transition: border-color .15s, box-shadow .15s;
}
.device-tile:hover { border-color: var(--no-primary); box-shadow: var(--no-shadow, 0 2px 8px rgba(16,24,40,.08)); }
.device-tile.is-offline { opacity: .72; }
.device-tile-head { display: flex; align-items: flex-start; gap: 10px; }
.device-tile-icon {
  width: 36px; height: 36px; border-radius: 8px; flex: 0 0 auto;
  background: var(--no-bg-active); color: var(--no-primary);
  display: flex; align-items: center; justify-content: center;
}
.device-tile-title { min-width: 0; flex: 1; display: flex; flex-direction: column; gap: 2px; }
.device-tile-name { font-size: 14px; font-weight: 600; color: var(--no-text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.device-tile-type { font-size: 12px; color: var(--no-text-muted); }
.device-tile-body { display: flex; flex-direction: column; gap: 5px; border-top: 1px solid var(--no-border-light); padding-top: 10px; }
.device-tile-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 12px; }
.device-tile-row span { color: var(--no-text-muted); }
.device-tile-row b { color: var(--no-text); font-weight: 500; overflow-wrap: anywhere; text-align: right; }
.device-tile-reading { border-top: 1px solid var(--no-border-light); padding-top: 10px; display: flex; flex-direction: column; gap: 3px; }
.device-tile-reading .reading-label { font-size: 11px; color: var(--no-text-muted); }
.device-tile-reading .reading-value { font-size: 14px; font-weight: 600; color: var(--no-text); overflow-wrap: anywhere; }
.device-tile-reading .reading-time { font-size: 11px; color: var(--no-text-muted); }
.device-tile-reading.no-data .reading-none { font-size: 12px; color: var(--no-text-muted); }
.device-row .chan-name { min-width: 0; flex: 1 1 22%; }
.device-row .chan-sub { flex: 0 1 16%; min-width: 0; }
.device-row .chan-badge { flex: 0 0 auto; }
.device-card-actions { display: flex; align-items: center; gap: 8px; }
</style>

<style scoped src="./nodeOverview.shared.css"></style>
