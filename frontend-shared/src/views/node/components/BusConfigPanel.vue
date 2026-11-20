<template>
  <div class="bus-page">
    <div class="bus-alert" :class="{ 'bus-alert-offline': nodeOffline }">
      <el-icon :size="16"><WarningFilled /></el-icon>
      <span><b>仅在线可编辑：</b>{{ nodeOnline ? '当前设备在线，您可以查看和修改配置' : '当前设备离线，只能查看已上报的资源配置' }}</span>
    </div>

    <section class="card bus-workbench">
      <div class="bus-toolbar">
        <div class="bus-seg" role="tablist" aria-label="总线类型">
          <button
            v-for="bus in busTabs"
            :key="bus.type"
            class="bus-seg-btn"
            :class="{ active: activeBusType === bus.type }"
            type="button"
            role="tab"
            :aria-selected="activeBusType === bus.type"
            @click="selectBusType(bus.type)"
          >
            <el-icon :size="14"><component :is="bus.icon" /></el-icon>
            {{ bus.label }}
            <span class="bus-seg-num">{{ busResourceCount(bus.type) }}</span>
          </button>
        </div>
        <div class="bus-toolbar-right">
          <label class="bus-search">
            <el-icon :size="14"><Search /></el-icon>
            <input v-model="resourceQuery" type="search" placeholder="搜索资源" aria-label="搜索资源" />
          </label>
          <button class="btn btn-plain btn-sm" type="button" :disabled="nodeOffline || resourceQuerying" @click="requestRefresh">
            <el-icon :size="13"><RefreshRight /></el-icon>{{ resourceQuerying ? '查询中…' : '查询资源' }}
          </button>
          <button
            class="btn btn-primary btn-sm"
            type="button"
            :disabled="nodeOffline || !selectedResource || selectedResource.enabled === false || !busSupportsChannels"
            @click="selectedResource ? openChannelManager(selectedResource) : undefined"
          >
            <el-icon :size="13"><Plus /></el-icon>新建通道
          </button>
        </div>
      </div>

      <div v-if="busLoading" class="bus-loading"><el-skeleton :rows="6" animated /></div>
      <div v-else-if="busLoadError" class="bus-load-error">
        <el-empty description="总线资源加载失败">
          <button class="btn btn-plain" type="button" @click="retryLoad">重试</button>
        </el-empty>
      </div>
      <div v-else class="bus-body">
        <aside class="bus-rail">
          <div class="bus-rail-head"><b>资源</b><span class="bus-rail-count">{{ filteredResources.length }}</span><span class="bus-rail-src">设备上报</span></div>
          <div v-if="filteredResources.length === 0" class="bus-rail-empty">
            {{ activeBusResources.length === 0 ? '该节点未上报此类型总线资源' : '没有匹配的资源' }}
          </div>
          <button
            v-for="resource in filteredResources"
            :key="resource.id"
            class="bus-res-card"
            :class="{ active: selectedResourceId === resource.id, disabled: resource.enabled === false }"
            type="button"
            :data-resource-id="resource.id"
            :data-view-resource="resource.id"
            @click="selectResource(resource.id)"
          >
            <span class="bus-res-top">
              <span class="bus-res-name">{{ resource.id }}</span>
              <span class="bus-res-state" :class="resource.enabled === false ? 'off' : 'on'">{{ resource.enabled === false ? '禁用' : '可用' }}</span>
            </span>
            <span class="bus-res-pins">{{ resourcePins(resource) }}</span>
            <span class="bus-res-meta">{{ resourceMode(resource) }} · {{ resourceParamText(resource) }}</span>
            <span class="bus-res-badges">
              <span class="bus-mini-badge" :class="{ blue: resourceMountedChannels(resource).length > 0 }">通道 {{ resourceMountedChannels(resource).length }}</span>
              <span class="bus-mini-badge">{{ dmaSummary(resource) }}</span>
            </span>
          </button>
          <div class="bus-rail-foot">GPIO / PWM 引脚直控资源在「外设」中管理</div>
        </aside>

        <div class="bus-detail" data-bus-detail>
          <template v-if="selectedResource">
            <div class="bus-detail-head">
              <div class="bus-detail-title">
                <b>{{ selectedResource.id }}</b>
                <span class="bus-type-tag">{{ activeBusTab.label }} 资源</span>
                <span class="bus-state-tag" :class="selectedResource.enabled === false ? 'off' : 'on'">{{ selectedResource.enabled === false ? '禁用' : '可用' }}</span>
              </div>
              <div class="bus-detail-sub">设备上报的物理资源 · 只读；通道会随配置下发到节点</div>
              <div class="bus-detail-actions">
                <button v-if="activeBusType === 'i2c'" class="btn btn-plain btn-sm" type="button" :disabled="nodeOffline || i2cScanning" @click="scanSelectedI2C">
                  <el-icon :size="13"><Search /></el-icon>{{ i2cScanning ? '扫描中…' : '地址扫描' }}
                </button>
                <span class="bus-dma-inline">
                  DMA
                  <el-select
                    v-if="selectedResource.enabled !== false && busSupportsDma && resourceDmaCandidates(selectedResource).length > 1"
                    class="dma-select"
                    size="small"
                    :data-dma-select="selectedResource.id"
                    :data-dma-detail-select="selectedResource.id"
                    :aria-label="`${selectedResource.id} 绑定的 DMA 资源`"
                    :model-value="dmaSelectionFor(selectedResource)"
                    :disabled="nodeOffline || !canToggleResourceDma(selectedResource)"
                    placeholder="未绑定"
                    @change="(value: any) => changeResourceDma(selectedResource!, String(value))"
                  >
                    <el-option label="不使用 DMA" value="" />
                    <el-option
                      v-for="dma in resourceDmaCandidates(selectedResource)"
                      :key="dma.dma_id"
                      :label="isDmaBoundToResource(dma, selectedResource) ? dmaBoundValueLabel(dma) : dmaOptionLabel(dma)"
                      :value="String(dma.dma_id)"
                      :disabled="!isDmaRebindable(dma.state, dma.bound_to) && !isDmaBoundToResource(dma, selectedResource)"
                    />
                  </el-select>
                  <el-switch
                    v-else-if="selectedResource.enabled !== false && busSupportsDma && resourceDmaCandidates(selectedResource).length === 1"
                    :aria-label="`${selectedResource.id} 的 DMA 绑定开关（${resourceDmaCandidates(selectedResource)[0].name}）`"
                    :model-value="resourceDmaBinding(selectedResource) ? true : false"
                    size="small"
                    :disabled="nodeOffline || !canToggleResourceDma(selectedResource)"
                    :loading="resourceDmaBinding(selectedResource) ? Boolean(dmaToggling[resourceDmaBinding(selectedResource)!.dma_id]) : false"
                    @change="(value: any) => toggleResourceDma(selectedResource!, value)"
                  />
                  <span v-else-if="!busSupportsDma || selectedResource.enabled === false" class="dma-na">—</span>
                  <span v-else class="dma-none" :data-dma-none="selectedResource.id">{{ resourceDmaUnavailableText() }}</span>
                </span>
              </div>
            </div>

            <div class="bus-meta">
              <div><span>引脚</span><b class="mono">{{ resourcePins(selectedResource) }}</b></div>
              <div><span>工作模式</span><b>{{ resourceMode(selectedResource) }}</b></div>
              <div><span>关键参数</span><b>{{ resourceParamText(selectedResource) }}</b></div>
              <div><span>已挂载通道</span><b>{{ resourceMountedChannels(selectedResource).length }} 条</b></div>
            </div>

            <div v-if="scanResult !== null" class="bus-scan-result">
              <b>扫描完成：{{ scanResult.length ? `发现 ${scanResult.length} 个地址` : '未发现设备' }}</b>
              <span v-for="addr in scanResult" :key="addr" class="bus-addr">{{ addr }}</span>
            </div>

            <div class="bus-section-head" data-created-channels>
              <h3>该资源下的通道</h3>
              <span class="bus-count" data-created-channels-count>{{ resourceMountedChannels(selectedResource).length }} 条</span>
              <button class="link-btn" type="button" :disabled="nodeOffline || !busSupportsChannels" @click="openChannelManager(selectedResource)">
                <el-icon :size="12"><Plus /></el-icon>新建通道
              </button>
            </div>

            <div v-if="resourceMountedChannels(selectedResource).length === 0" class="bus-empty-channels" data-created-channels-empty>
              <span class="bus-empty-icon"><el-icon :size="20"><Link /></el-icon></span>
              <b>还没有通道</b>
              <p>通道是这条资源上的采集实例。创建后会随配置下发，节点按通道采集数据。</p>
              <button class="btn btn-primary" type="button" :disabled="nodeOffline || !busSupportsChannels" @click="openChannelManager(selectedResource)">在此资源上新建通道</button>
            </div>

            <div v-else class="bus-channel-table">
              <table>
                <thead><tr><th>通道</th><th>参数</th><th>设备</th><th>状态</th><th class="bus-channel-actions-head">操作</th></tr></thead>
                <tbody>
                  <tr v-for="ch in resourceMountedChannels(selectedResource)" :key="ch.id" :data-channel-id="ch.id" :data-resource-id="selectedResource.id" class="bus-channel-item">
                    <td>
                      <div class="bus-channel-name">{{ channelName(ch) }}</div>
                      <div class="bus-channel-sub">#{{ ch.id }} · {{ channelHardwareKey(ch) }}</div>
                    </td>
                    <td class="mono">{{ channelParamText(ch) }}</td>
                    <td>{{ channelDeviceCount(ch) }} 台</td>
                    <td><span class="bus-channel-state" :class="channelEnabled(ch) ? 'on' : 'off'">{{ channelEnabled(ch) ? '正常' : '已停用' }}</span></td>
                    <td class="bus-channel-actions">
                      <button v-if="activeBusType === 'adc'" class="link-btn" type="button" :data-read-adc="ch.id" :disabled="nodeOffline || adcReadingId === ch.id" @click="readAdc(ch)">{{ adcReadingId === ch.id ? '读取中…' : '读取' }}</button>
                      <button class="link-btn" type="button" :data-edit-channel="ch.id" :disabled="nodeOffline" @click="editChannel(ch)">配置</button>
                      <button class="link-btn bus-danger" type="button" :disabled="nodeOffline" @click="deleteChannel(ch)">删除</button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div v-if="activeBusType === 'adc' && adcResult" class="bus-adc-result" data-adc-result role="status">
              <b>{{ adcResult.hardwareId }}</b>
              <span>raw {{ adcResult.raw }} / {{ adcResult.maxRaw }}</span>
              <span>{{ adcResult.percent.toFixed(2) }}%</span>
              <span class="bus-adc-time">刚读取</span>
            </div>
            <div v-else-if="activeBusType === 'adc' && adcError" class="bus-adc-result is-error" role="alert">{{ adcError }}</div>

            <div class="bus-detail-note">资源名称、引脚与参数由设备上报，页面不能修改；DMA 绑定与通道配置属于可下发的配置项。</div>
          </template>
          <el-empty v-else description="请选择左侧资源" :image-size="72" />
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { Connection, Cpu, DataLine, Link, MagicStick, Plus, RefreshRight, Search, WarningFilled } from '@element-plus/icons-vue'
import type { Capabilities, DmaChannelInfo } from '@/api/node'
import type { Channel } from '@/api/channel'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { isDmaRebindable } from '@/utils/dmaState'
import {
  type BusType, type BusResource, resourcesOf, resourcePins as pinsOf, resourceParameters as parametersOf,
  resourceMode as modeOf, resourceMountedChannels as mountedChannelsOf, dmaCandidatesOf, resourceDmaBindingOf,
  dmaSelectionOf, isDmaBoundToResource as isDmaBoundToResourceOf, canToggleResourceDma as canToggleResourceDmaOf,
  resourceDmaUnavailableText as dmaUnavailableTextOf, dmaOptionLabel, dmaBoundValueLabel,
} from '../composables/busResourceHelpers'
import { baudCurrentRate, channelEnabled, channelHardwareKey, channelName, channelTypeLabel, isUARTChannel } from '../composables/channelDisplay'

const props = defineProps<{
  nodeOnline: boolean
  nodeOffline: boolean
  capabilities: Capabilities
  channels: Channel[]
  devices?: Array<{ channel_id?: number }>
  dmaChannels: DmaChannelInfo[]
  dmaToggling: Record<number, boolean>
  activeBusType: BusType
  selectedResourceId: string
  busLoading: boolean
  busLoadError: boolean
  resourceQuerying: boolean
  i2cScanning: boolean
  scanResult: string[] | null
  adcReadingId?: number | null
  adcResult?: { channelId: number; hardwareId: string; raw: number; maxRaw: number; bitWidth: number; percent: number; at: number } | null
  adcError?: string
}>()

const emit = defineEmits<{
  'refresh-resources': []; 'retry-load': []; scan: []; 'navigate-channels': []
  'select-bus-type': [type: BusType]; 'select-resource': [id: string]
  'change-dma': [resource: BusResource, value: string]
  'toggle-dma': [resource: BusResource, enabled: string | number | boolean]
  'open-channel-manager': [resource: BusResource]
  'edit-channel': [channel: Channel]
  'delete-channel': [channel: Channel]
  'read-adc': [channel: Channel]
}>()

const busTabs: Array<{ type: BusType; label: string; icon: any; description: string }> = [
  { type: 'i2c', label: getHardwareLabel('i2c'), icon: Cpu, description: 'I2C 总线用于连接低速外设，支持多主多从通信' },
  { type: 'uart', label: getHardwareLabel('uart'), icon: Connection, description: 'UART 串口用于异步串行通信' },
  { type: 'spi', label: getHardwareLabel('spi'), icon: MagicStick, description: 'SPI 总线用于高速同步串行通信' },
  { type: 'adc', label: getHardwareLabel('adc'), icon: DataLine, description: 'ADC 通道用于采集模拟量输入' },
]
const activeBusTab = computed(() => busTabs.find(tab => tab.type === props.activeBusType) || busTabs[0])
const activeBusResources = computed<BusResource[]>(() => resourcesOf(props.capabilities, props.activeBusType))
const selectedResource = computed(() => activeBusResources.value.find(resource => resource.id === props.selectedResourceId) || null)
const busSupportsDma = computed(() => ['uart', 'i2c', 'spi'].includes(props.activeBusType))
const busSupportsChannels = computed(() => ['uart', 'i2c', 'spi', 'adc'].includes(props.activeBusType))

const resourceQuery = ref('')
const filteredResources = computed(() => {
  const q = resourceQuery.value.trim().toLowerCase()
  if (!q) return activeBusResources.value
  return activeBusResources.value.filter(resource => {
    const haystack = [resource.id, resourcePins(resource), ...resourceParameters(resource)].join(' ').toLowerCase()
    return haystack.includes(q)
  })
})

function busResourceCount(type: BusType): number { return resourcesOf(props.capabilities, type).length }
function resourcePins(resource: BusResource): string { return pinsOf(props.activeBusType, resource) }
function resourceParameters(resource: BusResource): string[] { return parametersOf(props.activeBusType, resource, props.channels) }
function resourceMode(resource: BusResource): string {
  const mode = modeOf(resource)
  if (mode !== '—') return mode
  return props.activeBusType === 'uart' ? '异步串口' : mode
}
function resourceParamText(resource: BusResource): string {
  return resourceParameters(resource).map(item => item.replace('（未创建通道）', '')).join(' · ') || '—'
}
function resourceMountedChannels(resource: BusResource): Channel[] { return mountedChannelsOf(props.activeBusType, resource, props.channels) }
function resourceDmaCandidates(_resource: BusResource): DmaChannelInfo[] { return dmaCandidatesOf(props.activeBusType, props.dmaChannels) }
function resourceDmaBinding(resource: BusResource): DmaChannelInfo | undefined { return resourceDmaBindingOf(props.activeBusType, resource, props.dmaChannels) }
function dmaSelectionFor(resource: BusResource): string { return dmaSelectionOf(props.activeBusType, resource, props.dmaChannels) }
function isDmaBoundToResource(dma: DmaChannelInfo, resource: BusResource): boolean { return isDmaBoundToResourceOf(props.activeBusType, dma, resource) }
function canToggleResourceDma(resource: BusResource): boolean { return canToggleResourceDmaOf(props.activeBusType, resource, props.dmaChannels) }
function resourceDmaUnavailableText(): string { return dmaUnavailableTextOf(props.activeBusType, props.dmaChannels) }
function dmaSummary(resource: BusResource): string {
  if (!busSupportsDma.value || resource.enabled === false) return 'DMA —'
  const bound = resourceDmaBinding(resource)
  return bound ? `DMA ${bound.name || 'CH' + bound.dma_id}` : 'DMA 未绑定'
}
function channelDeviceCount(ch: Channel): number {
  return (props.devices || []).filter(device => device.channel_id === ch.id).length
}
function channelParamText(ch: Channel): string {
  if (isUARTChannel(ch)) {
    const baud = baudCurrentRate(ch)
    return baud === null ? '波特率未配置' : `${baud} bit/s`
  }
  return channelTypeLabel(ch)
}

function selectBusType(type: BusType) { emit('select-bus-type', type) }
function selectResource(id: string) { emit('select-resource', id) }
function changeResourceDma(resource: BusResource, value: string) { emit('change-dma', resource, value) }
function toggleResourceDma(resource: BusResource, enabled: string | number | boolean) { emit('toggle-dma', resource, enabled) }
function scanSelectedI2C() { emit('scan') }
function requestRefresh() { emit('refresh-resources') }
function retryLoad() { emit('retry-load') }
function openChannelManager(resource: BusResource) { emit('open-channel-manager', resource) }
function editChannel(channel: Channel) { emit('edit-channel', channel) }
function readAdc(channel: Channel) { emit('read-adc', channel) }
function deleteChannel(channel: Channel) { emit('delete-channel', channel) }
</script>

<style scoped>
.bus-page { display: flex; flex-direction: column; gap: 12px; }
.bus-alert { min-height: 36px; display: flex; align-items: center; gap: 8px; padding: 6px 12px; color: var(--no-text-secondary); background: var(--no-warning-bg); border: 1px solid var(--no-warning-border); border-radius: 8px; font-size: 12px; }
.bus-alert > .el-icon { color: var(--no-warning); flex: none; }
.bus-alert-offline { background: var(--no-chip-off-bg); border-color: var(--no-border); }
.bus-workbench { overflow: hidden; display: flex; flex-direction: column; min-height: 560px; }
.bus-toolbar { min-height: 52px; padding: 8px 14px; border-bottom: 1px solid var(--no-border-light); display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.bus-seg { display: inline-flex; align-items: center; gap: 2px; background: var(--no-bg-subtle, #F2F4F7); border-radius: 8px; padding: 3px; max-width: 100%; overflow-x: auto; scrollbar-width: none; }
.bus-seg::-webkit-scrollbar { display: none; }
.bus-seg-btn { height: 28px; padding: 0 10px; border: 0; background: transparent; border-radius: 6px; color: var(--no-text-secondary); display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 500; font-family: inherit; cursor: pointer; transition: background .16s ease, color .16s ease; }
.bus-seg-btn:hover { color: var(--no-primary); }
.bus-seg-btn.active { background: var(--card-bg, #fff); color: var(--no-text); box-shadow: 0 1px 2px rgba(16, 24, 40, .10); }
.bus-seg-num { min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px; background: rgba(31, 35, 41, .06); color: var(--no-text-muted); font-size: 11px; display: inline-flex; align-items: center; justify-content: center; }
.bus-seg-btn.active .bus-seg-num { background: var(--no-bg-active); color: var(--no-primary); }
.bus-toolbar-right { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.bus-search { display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 9px; border: 1px solid var(--no-border); border-radius: 6px; background: var(--card-bg, #fff); color: var(--no-text-muted); min-width: 170px; }
.bus-search input { border: 0; outline: 0; background: transparent; width: 100%; font-size: 12px; color: var(--no-text); }
.bus-search input::placeholder { color: var(--no-text-muted); }
.bus-loading { padding: 18px; }
.bus-load-error { padding: 18px; }
.bus-body { display: grid; grid-template-columns: 300px minmax(0, 1fr); flex: 1; min-height: 0; }
.bus-rail { background: var(--no-bg-subtle, #FBFCFE); border-right: 1px solid var(--no-border-light); padding: 10px; display: flex; flex-direction: column; gap: 8px; min-height: 0; }
.bus-rail-head { display: flex; align-items: center; gap: 6px; padding: 2px 4px 6px; color: var(--no-text-muted); font-size: 12px; }
.bus-rail-head b { color: var(--no-text); font-weight: 600; }
.bus-rail-count { min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: var(--no-chip-off-bg); color: var(--no-text-secondary); font-size: 11px; display: inline-flex; align-items: center; justify-content: center; }
.bus-rail-src { margin-left: auto; font-size: 11px; color: var(--no-text-muted); }
.bus-rail-empty { padding: 24px 10px; text-align: center; color: var(--no-text-muted); font-size: 12px; line-height: 18px; }
.bus-res-card { width: 100%; text-align: left; border: 1px solid transparent; background: transparent; border-radius: 8px; padding: 10px 11px; display: flex; flex-direction: column; gap: 3px; font-family: inherit; cursor: pointer; position: relative; transition: background .16s ease, border-color .16s ease, box-shadow .16s ease, transform .16s ease; }
.bus-res-card:hover { background: var(--no-bg-hover); transform: translateY(-1px); }
.bus-res-card.active { background: var(--card-bg, #fff); border-color: var(--no-primary); box-shadow: 0 1px 3px rgba(46, 107, 255, .12); transform: none; }
.bus-res-card.active::before { content: ''; position: absolute; left: 0; top: 9px; bottom: 9px; width: 3px; border-radius: 2px; background: var(--no-primary); }
.bus-res-card.disabled { opacity: .58; }
.bus-res-top { display: flex; align-items: center; gap: 6px; }
.bus-res-name { font-size: 14px; font-weight: 600; color: var(--no-text); }
.bus-res-state { margin-left: auto; height: 18px; padding: 0 6px; border-radius: 9px; font-size: 11px; display: inline-flex; align-items: center; }
.bus-res-state.on { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-res-state.off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.bus-res-pins { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: var(--no-text-secondary); }
.bus-res-meta { font-size: 12px; color: var(--no-text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.bus-res-badges { display: flex; gap: 5px; margin-top: 4px; flex-wrap: wrap; }
.bus-mini-badge { height: 18px; padding: 0 6px; border-radius: 5px; background: var(--no-chip-off-bg); color: var(--no-text-secondary); font-size: 11px; display: inline-flex; align-items: center; }
.bus-mini-badge.blue { background: var(--no-bg-active); color: var(--no-primary); }
.bus-rail-foot { margin-top: auto; padding: 8px 4px 2px; border-top: 1px solid var(--no-border-light); font-size: 11px; color: var(--no-text-muted); }
.bus-detail { padding: 16px 20px; min-width: 0; display: flex; flex-direction: column; }
.bus-detail-head { display: flex; flex-direction: column; gap: 2px; position: relative; padding-right: 0; }
.bus-detail-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.bus-detail-title b { font-size: 16px; font-weight: 600; color: var(--no-text); }
.bus-type-tag { height: 20px; padding: 0 7px; border-radius: 5px; background: var(--no-bg-active); color: var(--no-primary); font-size: 11px; display: inline-flex; align-items: center; }
.bus-state-tag { height: 20px; padding: 0 7px; border-radius: 10px; font-size: 11px; display: inline-flex; align-items: center; }
.bus-state-tag.on { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-state-tag.off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.bus-detail-sub { font-size: 12px; color: var(--no-text-muted); }
.bus-detail-actions { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-top: 10px; }
.bus-dma-inline { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--no-text-muted); }
.bus-dma-inline .dma-select { width: 190px; }
.bus-meta { display: grid; grid-template-columns: minmax(0, .85fr) minmax(0, .85fr) minmax(0, 1.5fr) minmax(0, .85fr); gap: 10px; background: var(--no-bg-subtle, #F7F9FC); border: 1px solid var(--no-border-light); border-radius: 8px; padding: 10px 14px; margin-top: 12px; }
.bus-meta span { display: block; font-size: 11px; color: var(--no-text-muted); margin-bottom: 2px; }
.bus-meta b { font-size: 12px; font-weight: 500; color: var(--no-text); word-break: break-all; }
.bus-scan-result { margin-top: 10px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 8px 10px; border: 1px solid var(--no-primary); border-radius: 7px; background: var(--no-bg-active); font-size: 12px; color: var(--no-text-secondary); }
.bus-scan-result b { color: var(--no-text); }
.bus-adc-result { margin-top: 10px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 10px; border: 1px solid var(--no-primary); border-radius: 7px; background: var(--no-bg-active); font-size: 12px; color: var(--no-text-secondary); }
.bus-adc-result b { color: var(--no-text); }
.bus-adc-result.is-error { border-color: var(--el-color-danger-light-5); color: var(--el-color-danger); }
.bus-adc-time { color: var(--no-text-muted); }
.bus-addr { height: 24px; padding: 0 8px; border-radius: 5px; background: var(--card-bg, #fff); border: 1px solid var(--no-border); color: var(--no-primary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; display: inline-flex; align-items: center; }
.bus-section-head { display: flex; align-items: center; gap: 8px; margin: 18px 0 8px; }
.bus-section-head h3 { margin: 0; font-size: 14px; font-weight: 600; color: var(--no-text); }
.bus-count { font-size: 12px; color: var(--no-text-muted); }
.bus-section-head .link-btn { margin-left: auto; }
.bus-empty-channels { border: 1px dashed var(--no-border); border-radius: 8px; padding: 24px 16px; text-align: center; background: var(--no-bg-subtle, #FCFDFF); }
.bus-empty-icon { width: 40px; height: 40px; border-radius: 50%; background: var(--no-bg-active); color: var(--no-primary); display: inline-flex; align-items: center; justify-content: center; margin-bottom: 8px; }
.bus-empty-channels b { display: block; font-size: 13px; font-weight: 600; color: var(--no-text); margin-bottom: 2px; }
.bus-empty-channels p { margin: 0 0 12px; font-size: 12px; color: var(--no-text-secondary); line-height: 18px; }
.bus-channel-table { overflow-x: auto; border: 1px solid var(--no-border-light); border-radius: 8px; }
.bus-channel-table table { width: 100%; border-collapse: collapse; font-size: 13px; }
.bus-channel-table th { height: 36px; padding: 0 10px; text-align: left; font-size: 12px; font-weight: 500; color: var(--no-text-secondary); background: var(--no-bg-subtle, #F7F8FA); border-bottom: 1px solid var(--no-border-light); white-space: nowrap; }
.bus-channel-table td { padding: 9px 10px; border-bottom: 1px solid var(--no-border-light); vertical-align: middle; }
.bus-channel-table tbody tr:last-child td { border-bottom: 0; }
.bus-channel-table tbody tr { transition: background .14s ease; }
.bus-channel-table tbody tr:hover { background: var(--no-bg-hover); }
.bus-channel-name { font-weight: 500; color: var(--no-text); }
.bus-channel-sub { margin-top: 1px; font-size: 11px; color: var(--no-text-muted); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.bus-channel-state { height: 20px; padding: 0 7px; border-radius: 10px; font-size: 11px; display: inline-flex; align-items: center; }
.bus-channel-state.on { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-channel-state.off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.bus-channel-actions { text-align: right; white-space: nowrap; }
.bus-channel-actions .link-btn + .link-btn { margin-left: 8px; }
.bus-danger { color: var(--no-danger); }
.bus-detail-note { margin-top: auto; padding-top: 12px; font-size: 12px; color: var(--no-text-muted); line-height: 18px; }
.dma-na { color: var(--no-text-muted); }
.dma-none { color: var(--no-text-muted); font-size: 12px; }
.dma-select { width: 100%; min-width: 0; }
.dma-select :deep(.el-select__wrapper) { min-height: 28px; box-shadow: 0 0 0 1px var(--no-border) inset; }
@media (max-width: 980px) {
  .bus-body { grid-template-columns: 1fr; }
  .bus-rail { border-right: 0; border-bottom: 1px solid var(--no-border-light); flex-direction: row; overflow-x: auto; }
  .bus-rail-head, .bus-rail-foot { display: none; }
  .bus-res-card { min-width: 220px; }
  .bus-seg { flex-wrap: nowrap; }
  .bus-meta { grid-template-columns: 1fr 1fr; }
  .bus-channel-table table { min-width: 640px; }
}
</style>

<style scoped src="./nodeOverview.shared.css"></style>
