<template>
  <section class="card dma-page dma-card">
    <div class="dma-toolbar">
      <div class="dma-sum">
        <span class="dma-sum-item"><b>{{ dmaChannels.length }}</b>DMA 通道</span>
        <span class="dma-sum-item ok"><b>{{ counts.allocated }}</b>已分配</span>
        <span class="dma-sum-item"><b>{{ counts.free }}</b>空闲</span>
        <span class="dma-sum-item"><b>{{ counts.disabled }}</b>已禁用</span>
      </div>
      <div class="dma-filters" role="group" aria-label="DMA 状态筛选">
        <button
          v-for="filter in filters"
          :key="filter.key"
          class="dma-chip"
          :class="{ active: activeFilter === filter.key }"
          type="button"
          @click="activeFilter = filter.key"
        >
          {{ filter.label }}<span class="dma-chip-num">{{ counts[filter.key] }}</span>
        </button>
      </div>
      <button class="btn btn-plain btn-sm dma-refresh" type="button" :loading="dmaLoading" @click="refresh">
        <el-icon :size="13"><RefreshRight /></el-icon>刷新
      </button>
      <button class="btn btn-plain btn-sm" type="button" :disabled="offline || selftestLoading" @click="runSelftest">
        {{ selftestLoading ? '自检中…' : 'DMA 自检' }}
      </button>
    </div>
    <div v-if="selftestResult" class="dma-selftest" :class="{ ok: selftestResult.passed }">
      <b>{{ selftestResult.passed ? 'GDMA 自检通过' : 'GDMA 自检失败' }}</b>
      <span>{{ selftestResult.bytes }} B · checksum {{ selftestResult.checksum }}</span>
    </div>

    <div v-if="dmaLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
    <div v-else-if="dmaChannels.length === 0" class="dma-empty" data-dma-empty>
      <template v-if="dmaNotReported">该节点未上报 DMA 能力 —— 这不是「没有 DMA 通道」，而是本节点未提供该能力信息。</template>
      <template v-else>该节点已上报 {{ dmaReportedCount }} 条 DMA 资源，但当前没有可用的 DMA 通道信息</template>
    </div>
    <div v-else class="dma-table-wrap">
      <table class="dma-table">
        <thead>
          <tr><th>通道</th><th>能力</th><th>最大突发</th><th>状态</th><th>绑定资源</th><th>可服务总线</th><th class="dma-actions-head">操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="dma in visibleChannels" :key="dma.dma_id">
            <td><div class="dma-name mono">{{ dma.name || 'DMA' + dma.dma_id }}</div><div class="dma-sub mono">CH{{ dma.dma_id }}</div></td>
            <td>{{ capText(dma.capabilities) }}</td>
            <td class="mono">{{ dma.max_burst }} B</td>
            <td><span class="dma-state" :class="dmaTagClass(dma.state)">{{ dmaStateText(dma.state) }}</span></td>
            <td>
              <button v-if="boundOf(dma)" class="link-btn" type="button" @click="gotoResource(dma)">
                {{ dmaBoundToText(dma.bound_to) }}
              </button>
              <span v-else class="dma-muted">未绑定</span>
              <div v-if="dma.device_allocated" class="dma-device-state ok">设备已确认分配</div>
              <div v-else-if="dma.state === 1" class="dma-device-state pending">配置已下发，等待设备确认</div>
            </td>
            <td class="dma-muted">{{ busText(dma.compatible_bus) }}</td>
            <td class="dma-actions">
              <button v-if="dma.state === 1" class="link-btn dma-danger" type="button" :disabled="offline || toggling[dma.dma_id]" @click="unbind(dma)">解绑</button>
              <button v-else-if="dma.state === 0" class="link-btn" type="button" @click="emit('goto-bus')">去绑定</button>
              <span v-else class="dma-muted">—</span>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="visibleChannels.length === 0" class="dma-filter-empty">当前筛选下没有 DMA 通道</div>
    </div>

    <div class="dma-notes">
      <div class="dma-note">
        <b>分配规则</b>
        <ul>
          <li>一条 DMA 同时只能服务一个资源；改绑会先解绑旧资源。</li>
          <li>资源未绑定 DMA 时回退到中断或轮询，功能正常、吞吐下降。</li>
          <li>已禁用的 DMA 不参与分配，也不会自动启用。</li>
        </ul>
      </div>
      <div class="dma-note">
        <b>与固件的关系</b>
        <p>DMA 不足不会让配置失败：固件会在配置回执中给出「降级」告警，页面在基本信息中以警告色提示。看到告警不代表采集异常。</p>
        <p>本页的分配是资源预留；UART 驱动当前仍为中断模式。真实 GDMA 引擎可用「DMA 自检」验证。</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { RefreshRight } from '@element-plus/icons-vue'
import { nodeApi, type Capabilities, type DmaChannelInfo } from '@/api/node'
import { useDmaStore } from '@/stores/dma'
import feedback from '@/utils/feedback'
import { dmaStateText } from '@/utils/dmaState'
import { busText, capText, dmaBoundToText, dmaTagClass } from '../composables/busResourceHelpers'
import { type BusType, type BusResource, resourcesOf, resourceDmaBindingOf } from '../composables/busResourceHelpers'

const props = defineProps<{
  dmaChannels: DmaChannelInfo[]
  dmaLoading: boolean
  capabilities: Capabilities
  nodeSerial: string
  nodeOffline: boolean
}>()

const emit = defineEmits<{
  'goto-resource': [type: BusType, id: string]
  'goto-bus': []
}>()

const dmaStore = useDmaStore()
const selftestLoading = ref(false)
const selftestResult = ref<{ passed: boolean; bytes: number; checksum: number } | null>(null)
const activeFilter = ref<'all' | 'free' | 'allocated' | 'disabled'>('all')
const filters = [
  { key: 'all' as const, label: '全部' },
  { key: 'free' as const, label: '空闲' },
  { key: 'allocated' as const, label: '已分配' },
  { key: 'disabled' as const, label: '已禁用' },
]
const offline = computed(() => props.nodeOffline)
const toggling = computed(() => dmaStore.toggling)

const counts = computed(() => {
  const result = { all: props.dmaChannels.length, free: 0, allocated: 0, disabled: 0 }
  for (const dma of props.dmaChannels) {
    if (dma.state === 0) result.free++
    else if (dma.state === 1) result.allocated++
    else if (dma.state === 2) result.disabled++
  }
  return result
})
const visibleChannels = computed(() => props.dmaChannels.filter(dma => {
  if (activeFilter.value === 'all') return true
  if (activeFilter.value === 'free') return dma.state === 0
  if (activeFilter.value === 'allocated') return dma.state === 1
  return dma.state === 2
}))

const dmaNotReported = computed(() => {
  const buses = (props.capabilities as any)?.buses
  return !buses || buses.dma === undefined || buses.dma === null || (Array.isArray(buses.dma) && buses.dma.length === 0)
})
const dmaReportedCount = computed(() => {
  const dma = (props.capabilities as any)?.buses?.dma
  return Array.isArray(dma) ? dma.length : 0
})

const TYPE_ORDER: BusType[] = ['uart', 'i2c', 'spi', 'adc']
function boundOf(dma: DmaChannelInfo): { type: BusType; resource: BusResource } | null {
  for (const type of TYPE_ORDER) {
    for (const resource of resourcesOf(props.capabilities, type)) {
      const binding = resourceDmaBindingOf(type, resource, props.dmaChannels)
      if (binding && binding.dma_id === dma.dma_id) return { type, resource }
    }
  }
  return null
}
function gotoResource(dma: DmaChannelInfo) {
  const found = boundOf(dma)
  if (found) emit('goto-resource', found.type, found.resource.id)
}
async function unbind(dma: DmaChannelInfo) {
  if (props.nodeOffline) return
  const bound = boundOf(dma)
  const label = bound ? `${bound.resource.id}（${bound.type.toUpperCase()}）` : dmaBoundToText(dma.bound_to)
  const ok = await feedback.confirmDanger(
    `解绑 ${dma.name || 'DMA' + dma.dma_id}？解绑后 ${label} 将回退到中断或轮询模式，吞吐会下降，但采集不会中断。`,
    { title: '确认解绑 DMA', confirmText: '解绑', cancelText: '取消' },
  )
  if (!ok) return
  try {
    await dmaStore.toggle(props.nodeSerial, dma, false)
    ElMessage.success('DMA 已解绑')
    await refresh()
  } catch (error) {
    feedback.handleError(error, 'DMA 解绑失败')
  }
}
async function refresh() {
  if (!props.nodeSerial) return
  try {
    await dmaStore.fetch(props.nodeSerial)
  } catch (error) {
    feedback.handleError(error, '刷新 DMA 状态失败')
  }
}

async function runSelftest() {
  if (!props.nodeSerial || props.nodeOffline) return
  selftestLoading.value = true
  selftestResult.value = null
  try {
    const result = await nodeApi.dmaSelftest(props.nodeSerial)
    selftestResult.value = { passed: result.passed, bytes: result.bytes, checksum: result.checksum }
    if (result.passed) ElMessage.success(`GDMA 自检通过：${result.bytes} B，checksum ${result.checksum}`)
    else feedback.handleError(new Error('目的数据与源数据不一致'), 'GDMA 自检失败')
  } catch (error) {
    feedback.handleError(error, 'GDMA 自检失败')
  } finally {
    selftestLoading.value = false
  }
}
</script>

<style scoped>
.dma-page { overflow: hidden; }
.dma-device-state { margin-top: 2px; font-size: 11px; line-height: 15px; }
.dma-device-state.ok { color: var(--el-color-success); }
.dma-device-state.pending { color: var(--no-text-muted); }
.dma-selftest { display: flex; gap: 10px; align-items: center; padding: 8px 16px; border-bottom: 1px solid var(--no-border); font-size: 12px; color: var(--el-color-danger); }
.dma-selftest.ok { color: var(--el-color-success); }
.dma-toolbar { min-height: 56px; padding: 10px 16px; border-bottom: 1px solid var(--no-border-light); display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.dma-sum { display: flex; align-items: baseline; gap: 16px; flex-wrap: wrap; color: var(--no-text-secondary); font-size: 12px; }
.dma-sum-item { display: inline-flex; align-items: baseline; gap: 5px; }
.dma-sum-item b { font-size: 16px; font-weight: 600; color: var(--no-text); line-height: 22px; }
.dma-sum-item.ok b { color: var(--no-success-text); }
.dma-filters { display: inline-flex; align-items: center; gap: 6px; flex-wrap: wrap; margin-left: auto; }
.dma-chip { height: 28px; padding: 0 10px; border-radius: 14px; border: 1px solid var(--no-border); background: var(--card-bg, #fff); color: var(--no-text-secondary); font-size: 12px; display: inline-flex; align-items: center; gap: 5px; font-family: inherit; cursor: pointer; transition: background .16s ease, border-color .16s ease, color .16s ease; }
.dma-chip:hover { border-color: var(--no-primary); color: var(--no-primary); }
.dma-chip.active { background: var(--no-bg-active); border-color: var(--no-primary); color: var(--no-primary); font-weight: 500; }
.dma-chip-num { font-size: 11px; opacity: .75; }
.dma-refresh { flex: none; }
.dma-table-wrap { padding: 0 16px; overflow-x: auto; }
.dma-table { width: 100%; min-width: 760px; border-collapse: collapse; font-size: 13px; }
.dma-table th { height: 40px; padding: 0 10px; text-align: left; font-size: 12px; font-weight: 500; color: var(--no-text-secondary); background: var(--no-bg-subtle, #F7F8FA); border-bottom: 1px solid var(--no-border-light); white-space: nowrap; }
.dma-table td { height: 52px; padding: 8px 10px; border-bottom: 1px solid var(--no-border-light); vertical-align: middle; }
.dma-table tbody tr:last-child td { border-bottom: 0; }
.dma-table tbody tr { transition: background .14s ease; }
.dma-table tbody tr:hover { background: var(--no-bg-hover); }
.dma-name { font-weight: 600; color: var(--no-text); }
.dma-sub { font-size: 11px; color: var(--no-text-muted); margin-top: 1px; }
.dma-state { height: 20px; padding: 0 8px; border-radius: 10px; font-size: 11px; display: inline-flex; align-items: center; }
.dma-muted { color: var(--no-text-muted); }
.dma-actions { text-align: right; white-space: nowrap; }
.dma-danger { color: var(--no-danger); }
.dma-empty { padding: 40px 20px; text-align: center; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }
.dma-filter-empty { padding: 24px; text-align: center; color: var(--no-text-muted); font-size: 12px; }
.dma-notes { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; padding: 14px 16px 16px; }
.dma-note { background: var(--no-bg-subtle, #F7F9FC); border: 1px solid var(--no-border-light); border-radius: 8px; padding: 12px 14px; font-size: 12px; line-height: 19px; color: var(--no-text-secondary); }
.dma-note b { display: block; margin-bottom: 4px; color: var(--no-text); font-size: 13px; }
.dma-note ul { margin: 0; padding-left: 16px; }
.dma-note li { margin: 2px 0; }
.dma-note p { margin: 0; }
@media (max-width: 980px) {
  .dma-filters { margin-left: 0; }
  .dma-notes { grid-template-columns: 1fr; }
}
</style>

<style scoped src="./nodeOverview.shared.css"></style>
