<template>
  <el-dialog
    v-model="dialogVisible"
    title="恢复已删除设备"
    width="720px"
    align-center
    :close-on-click-modal="false"
    data-test="restore-dialog"
  >
    <el-alert type="info" :closable="false" show-icon class="restore-notice">
      <template #title>
        <span>
          恢复的是<strong>同一条</strong>边缘设备记录（id 不变）—— 它名下的历史数据从未被删除，
          恢复并重新绑定后立刻连续可查。这与「新建一台设备」是两件事：新建会得到新 id，
          旧数据仍然挂在旧 id 上。
          <br />
          注意：勾选过「同时删除历史数据」的设备，后台可能已开始清理（清理中不可恢复）；
          在清理开始前恢复会<strong>撤回</strong>数据删除请求。
        </span>
      </template>
    </el-alert>

    <el-alert v-if="listError" type="error" :closable="false" show-icon class="restore-error" data-test="restore-error">
      <template #title>
        <span>已删除设备列表加载失败：{{ listError }}</span>
        <el-button link type="primary" size="small" data-test="restore-retry" @click="load">重试</el-button>
      </template>
    </el-alert>

    <el-alert v-if="nodeError" type="warning" :closable="false" show-icon class="restore-error" data-test="restore-node-error">
      <template #title>
        <span>节点列表加载失败：{{ nodeError }}（可先浏览已删设备，稍后重试选择绑定位置）</span>
        <el-button link type="primary" size="small" data-test="restore-node-retry" @click="load">重试</el-button>
      </template>
    </el-alert>

    <el-skeleton v-if="loading && items.length === 0" :rows="4" animated />

    <!-- 移动端宽表：弹窗内表格在窄屏必然溢出，同样走 .mobile-table-wrapper
         （本仓门禁 MobileTableWrapper.spec.ts 要求每张 el-table 都在这条祖先链上）。 -->
    <div v-else class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        :data="items"
        v-loading="loading"
        max-height="340"
        :row-key="rowKeyOf"
        :expand-row-keys="expandRowKeys"
        empty-text="没有可恢复的已删除设备"
        data-test="restore-table"
      >
      <el-table-column type="expand">
        <template #default="{ row }">
          <div class="restore-bind" @click.stop>
            <div class="restore-original-line">
              原位置：节点 <strong>{{ originalNodeLabel(asDeleted(row)) }}</strong>
              · 通道 <strong>#{{ asDeleted(row).channel_id }}</strong>
              <span class="restore-original-hint">
                （原位置被占用时可换空闲槽位；若只想保住历史数据，也可改用「合并历史数据」）
              </span>
            </div>
            <el-alert
              v-if="asDeleted(row).purge_in_progress"
              type="error"
              :closable="false"
              show-icon
              class="restore-row-alert"
            >
              <template #title>该设备的历史数据正在被后台清理，<strong>此时无法恢复</strong>（清理开始后删除不可撤销）。</template>
            </el-alert>
            <el-alert
              v-else-if="asDeleted(row).data_purge_requested"
              type="warning"
              :closable="false"
              show-icon
              class="restore-row-alert"
            >
              <template #title>该设备删除时勾选过「同时删除历史数据」；现在恢复会<strong>撤回</strong>删除请求并保留历史。</template>
            </el-alert>
            <el-form label-width="96px" @submit.prevent>
              <el-form-item label="绑定节点" required>
                <el-select
                  v-model="bind[row.id].node_id"
                  placeholder="选择节点"
                  style="width: 100%"
                  data-test="restore-node"
                  @change="onNodeChange(row.id)"
                >
                  <el-option
                    v-for="c in nodes"
                    :key="String(c.node_id)"
                    :label="`${c.name} (${c.model || '未知型号'})`"
                    :value="c.node_id"
                  />
                </el-select>
              </el-form-item>
              <el-form-item label="绑定通道" required>
                <el-select
                  v-model="bind[row.id].channel_id"
                  :placeholder="channelPlaceholder(row.id)"
                  :loading="isChannelsLoading(row.id)"
                  style="width: 100%"
                  data-test="restore-channel"
                >
                  <el-option
                    v-for="ch in channelsFor(row.id)"
                    :key="ch.id"
                    :label="`${channelLabel(ch)} · ${getHardwareLabel(ch.hardware_type)}`"
                    :value="ch.id!"
                  />
                </el-select>
                <div v-if="nodeChannelsEmpty(row.id)" class="restore-channel-empty">
                  该节点下没有可用通道（需已启用、且为传输总线：hardware_type 与 bus_type 一致，不能是 GPIO/PWM 外设）。
                </div>
              </el-form-item>
              <el-form-item label="设备名称">
                <el-input v-model="bind[row.id].name" :placeholder="row.name || '保持原名'" />
              </el-form-item>
            </el-form>
            <el-button
              type="primary"
              size="small"
              :loading="restoringId === row.id"
              :disabled="!canRestore(asDeleted(row))"
              :title="asDeleted(row).purge_in_progress ? '后台已开始清理该设备数据，无法恢复' : undefined"
              data-test="restore-confirm"
              @click="onRestore(asDeleted(row))"
            >
              恢复到所选位置
            </el-button>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
      <el-table-column prop="device_type" label="类型" width="130" show-overflow-tooltip />
      <el-table-column label="最后数据" width="140">
        <template #default="{ row }">{{ formatRelativeTime(asDeleted(row).last_data_at) }}</template>
      </el-table-column>
      <el-table-column label="数据量估算" width="130">
        <template #default="{ row }">{{ formatEstimate(asDeleted(row)) }}</template>
      </el-table-column>
      <el-table-column label="删除时间" width="170">
        <template #default="{ row }">{{ formatTime(asDeleted(row).deleted_at) }}</template>
      </el-table-column>
      <el-table-column label="可恢复性" width="150">
        <template #default="{ row }">
          <el-tag v-if="asDeleted(row).purge_in_progress" type="danger" size="small" data-test="restore-purge-in-progress">
            数据清理中（不可恢复）
          </el-tag>
          <el-tag v-else-if="asDeleted(row).data_purge_requested" type="warning" size="small" data-test="restore-purge-requested">
            已请求删数据（可撤回）
          </el-tag>
          <el-tag v-else type="success" size="small">可恢复</el-tag>
        </template>
      </el-table-column>
      </el-table>
    </div>

    <div v-if="total > pageSize" class="restore-pagination">
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        data-test="restore-pagination"
        @current-change="onPageChange"
      />
    </div>

    <template #footer>
      <el-button @click="dialogVisible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
/**
 * 恢复已删除设备（契约 §4.2 / R0 解法②「直接复用」）。
 *
 * 为什么这个入口是「换节点后历史不丢」的关键路径之一：
 *   设备被软删后，unified_data/device_data 的行仍然挂在**同一个 device_id** 上。
 *   恢复 = 清除 deleted_at + 改绑定，不搬运任何数据行 ⇒ 历史立刻连续。
 *
 * 2026-10-25 与后端新契约对齐（用户反馈「恢复设备功能逻辑还有大问题」）：
 *   · /deleted 已分页并返回 data_purge_requested / purge_in_progress：
 *     分页必须消费 total（否则 >page_size 的已删设备被静默隐藏）；清理中的设备
 *     必须提前禁用恢复（后端会 409）。
 *   · restore 现在校验通道可服务性（enabled / 非 GPIO·PWM 外设 /
 *     hardware_type==bus_type）：前端提前过滤，避免用户选了才失败。
 *   · 通道列表改为**按节点本地缓存**（不再依赖全局 channelStore —— 它会被
 *     fetchChannels(nodeId) 整体替换，导致多行同时配置时前一行选项被清空）。
 *   · 不相关错误解耦：节点接口失败不拖垮已删设备列表（Promise.allSettled）。
 *   · 单飞提交：同一时刻只允许一个恢复请求（避免跨行切换 loading 后重复提交）。
 */
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { edgeDeviceApi, type DeletedEdgeDevice } from '@/api/edgeDevice'
import { channelApi, compactChannelList, type Channel } from '@/api/channel'
import { useNodeStore } from '@/stores/node'
import type { Node } from '@/api/node'
import { getHardwareLabel } from '@/utils/hardwareTag'

const props = defineProps<{
  visible: boolean
  /**
   * 打开时自动展开该设备行（历史设备列表的行操作「恢复」用）。
   * 找不到时静默忽略 —— 设备可能刚好被清理/恢复，不阻塞弹窗正常使用。
   */
  initialDeviceId?: number
}>()
const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'restored', deviceId: number): void
}>()

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const nodeStore = useNodeStore()

const loading = ref(false)
const listError = ref('')
const nodeError = ref('')
const items = ref<DeletedEdgeDevice[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const restoringId = ref<number | null>(null)
const nodes = ref<Node[]>([])
/** el-table 行 key（统一字符串）：expand-row-keys 只接受 string[]，数字 id 需先转字符串。 */
const rowKeyOf = (row: unknown): string => String((row as { id?: unknown }).id ?? '')
/** el-table 展开行（受控）：历史设备列表点「恢复」时预展开对应行。 */
const expandRowKeys = ref<string[]>([])

/** 每行的待恢复绑定（行内展开表单），按设备 id 收敛，避免多行互相污染。 */
const bind = reactive<Record<number, { node_id: number | string | null; channel_id: number | null; name: string }>>({})

/** 按节点缓存的通道列表（本组件私有，不写全局 channelStore）。 */
const channelsByNode = reactive<Record<string, Channel[]>>({})
const channelsLoading = reactive<Record<string, boolean>>({})

const ensureBind = (id: number) => {
  if (!bind[id]) bind[id] = { node_id: null, channel_id: null, name: '' }
  return bind[id]
}

const resetBinds = () => {
  for (const key of Object.keys(bind)) delete bind[Number(key)]
  for (const it of items.value) ensureBind(it.id)
}

const isPeripheralType = (type?: string): boolean => {
  const t = String(type ?? '').trim().toUpperCase()
  return t === 'GPIO' || t === 'PWM'
}

/**
 * 通道可服务性过滤（与后端 restore 校验同一口径）：
 *   · 必须启用；
 *   · 必须是传输通道（GPIO/PWM 是外设资源，不能挂设备）；
 *   · hardware_type 与 bus_type 必须一致（大小写不敏感）。
 * 注：驱动声明的总线类型由后端最终校验（前端拿不到可靠映射时不误伤）。
 */
const isUsableChannel = (ch: Channel): boolean => {
  if (!ch || ch.enabled === false) return false
  const hw = String(ch.hardware_type ?? '').trim()
  const bus = String(ch.bus_type ?? '').trim()
  if (!hw || !bus) return false
  if (isPeripheralType(hw) || isPeripheralType(bus)) return false
  return hw.toUpperCase() === bus.toUpperCase()
}

const channelsFor = (id: number): Channel[] => {
  const nodeIdStr = String(ensureBind(id).node_id ?? '')
  if (!nodeIdStr) return []
  return (channelsByNode[nodeIdStr] ?? []).filter(isUsableChannel)
}

const isChannelsLoading = (id: number): boolean => {
  const nodeIdStr = String(ensureBind(id).node_id ?? '')
  return nodeIdStr ? channelsLoading[nodeIdStr] === true : false
}

const nodeChannelsEmpty = (id: number): boolean => {
  const nodeIdStr = String(ensureBind(id).node_id ?? '')
  return !!nodeIdStr && !channelsLoading[nodeIdStr] && channelsFor(id).length === 0
}

const channelPlaceholder = (id: number): string => {
  const nodeIdStr = String(ensureBind(id).node_id ?? '')
  if (!nodeIdStr) return '请先选择节点'
  return isChannelsLoading(id) ? '正在加载通道…' : '选择该节点下的可用通道'
}

const channelLabel = (ch: Channel): string => ch.name || `${ch.hardware_id ?? ''}`

/** 展示设备被删除前的原位置（节点名/序列号 + 通道 id），给"恢复原位置 vs 换槽位 vs 合并"提供判据。 */
const originalNodeLabel = (row: DeletedEdgeDevice): string => {
  const matched = nodes.value.find(n => String(n.node_id) === String(row.node_id))
  return matched ? `${matched.name || matched.node_id}（${matched.node_id}）` : String(row.node_id)
}

const loadChannelsFor = async (nodeIdStr: string) => {
  if (!nodeIdStr || channelsByNode[nodeIdStr]) return
  channelsLoading[nodeIdStr] = true
  try {
    const res = await channelApi.getList(nodeIdStr)
    channelsByNode[nodeIdStr] = compactChannelList(Array.isArray(res) ? res : (res as { items?: unknown }).items)
  } catch {
    channelsByNode[nodeIdStr] = []
  } finally {
    channelsLoading[nodeIdStr] = false
  }
  // 已选通道若在过滤后不可用，清空，避免提交一个界面上看不到的残留值。
  for (const idKey of Object.keys(bind)) {
    const id = Number(idKey)
    const b = bind[id]
    if (String(b.node_id ?? '') !== nodeIdStr) continue
    if (b.channel_id !== null && !channelsFor(id).some(ch => ch.id === b.channel_id)) {
      b.channel_id = null
    }
  }
}

const onNodeChange = (id: number) => {
  // 通道属于节点：换了节点，旧通道一定不属于新节点，必须重选。
  ensureBind(id).channel_id = null
  const nodeIdStr = String(ensureBind(id).node_id ?? '')
  if (nodeIdStr) void loadChannelsFor(nodeIdStr)
}

/** el-table 作用域槽的 row 是全局 DefaultRow 类型，此处收窄为真实行类型（非 any）。 */
const asDeleted = (row: unknown) => row as DeletedEdgeDevice

/** 数据量展示：row_estimate 为 null = 估算不出来，**不是 0 行**（显示「—」而不是「约 0 条」）。 */
const formatEstimate = (row: DeletedEdgeDevice): string => {
  const n = row.row_estimate
  return typeof n === 'number' ? `约 ${n.toLocaleString('zh-CN')} 条` : '—'
}

const formatTime = (t: string | null | undefined): string => {
  if (!t) return '—'
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString('zh-CN', { hour12: false })
}

const formatRelativeTime = (t: string | null | undefined): string => {
  if (!t) return '—'
  const ts = new Date(t).getTime()
  if (!Number.isFinite(ts)) return '—'
  const diff = Date.now() - ts
  if (diff < 0) return '刚刚'
  const min = Math.floor(diff / 60000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  return `${Math.floor(hour / 24)} 天前`
}

const load = async () => {
  loading.value = true
  listError.value = ''
  nodeError.value = ''
  const [deletedRes, nodesRes] = await Promise.allSettled([
    edgeDeviceApi.getDeleted({ page: page.value, page_size: pageSize.value }),
    nodeStore.fetchNodes({ page: 1, page_size: 200 }),
  ])
  if (deletedRes.status === 'fulfilled') {
    const res = deletedRes.value
    items.value = res.items
    total.value = res.total
    page.value = res.page
    pageSize.value = res.page_size || pageSize.value
    resetBinds()
    // 行操作「恢复」定位：只展开确实出现在当前页的设备（找不到静默）。
    const initialId = props.initialDeviceId
    expandRowKeys.value = typeof initialId === 'number' && items.value.some(it => it.id === initialId)
      ? [String(initialId)]
      : []
    // 当前页因恢复/清理变空但仍有其它页：回到第 1 页，避免"空白页"假死。
    if (items.value.length === 0 && total.value > 0 && page.value > 1) {
      page.value = 1
      loading.value = false
      return load()
    }
  } else {
    listError.value = deletedRes.reason instanceof Error ? deletedRes.reason.message : String(deletedRes.reason)
  }
  if (nodesRes.status === 'fulfilled') {
    nodes.value = nodeStore.getCachedList({ page: 1, page_size: 200 })?.items ?? []
  } else {
    nodeError.value = nodesRes.reason instanceof Error ? nodesRes.reason.message : String(nodesRes.reason)
  }
  loading.value = false
}

const onPageChange = (next: number) => {
  if (next === page.value) return
  page.value = next
  void load()
}

const canRestore = (row: DeletedEdgeDevice): boolean => {
  const b = ensureBind(row.id)
  return !row.purge_in_progress
    && restoringId.value === null
    && !!b.node_id
    && !!b.channel_id
}

const onRestore = async (row: DeletedEdgeDevice) => {
  if (row.purge_in_progress) {
    ElMessage.warning('该设备的历史数据正在被后台清理，无法恢复（清理开始后删除不可撤销）')
    return
  }
  if (restoringId.value !== null) return
  const b = ensureBind(row.id)
  if (!b.node_id || !b.channel_id) {
    ElMessage.warning('请选择绑定节点与通道')
    return
  }
  restoringId.value = row.id
  try {
    await edgeDeviceApi.restore(row.id, {
      node_id: String(b.node_id),
      channel_id: b.channel_id,
      ...(b.name.trim() ? { name: b.name.trim() } : {}),
    })
    ElMessage.success(`设备「${row.name || '#' + row.id}」已恢复，历史数据已连续`)
    emit('restored', row.id)
    await load()
  } catch (e) {
    feedback.handleError(e, '恢复设备失败')
  } finally {
    restoringId.value = null
  }
}

watch(() => props.visible, (open) => {
  if (!open) return
  // 打开即拉取：已删除设备是低频列表，缓存只会让用户看到已恢复的旧行。
  // 每次打开重置分页与行内绑定，避免上次的（可能已失效的）选择残留。
  page.value = 1
  expandRowKeys.value = []
  for (const key of Object.keys(bind)) delete bind[Number(key)]
  void load()
}, { immediate: true })
</script>

<style scoped>
.restore-notice {
  margin-bottom: 12px;
}
.restore-error {
  margin-bottom: 12px;
}
.restore-bind {
  padding: 8px 16px 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.restore-original-line {
  font-size: 12px;
  color: var(--el-text-color-regular);
}
.restore-original-hint {
  color: var(--el-text-color-secondary);
}
.restore-row-alert {
  margin-bottom: 4px;
}
.restore-channel-empty {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-color-warning);
}
.restore-pagination {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>
