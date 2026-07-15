<template>
  <div class="channel-page">
    <PageHeader title="通道管理" subtitle="查看和管理所有节点的通道配置">
      <template #extra>
        <el-button @click="refreshData" :loading="loading">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </template>
    </PageHeader>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索通道名称、硬件ID..."
          prefix-icon="Search"
          clearable
          class="search-input"
          @input="handleSearch"
        />

        <el-select
          v-model="nodeFilter"
          placeholder="节点筛选"
          clearable
          @change="handleFilter"
          style="min-width: 160px;"
        >
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部节点" value="" />
          <el-option
            v-for="node in nodeOptions"
            :key="node.id"
            :label="node.name"
            :value="node.id"
          />
        </el-select>

        <el-select
          v-model="hardwareTypeFilter"
          placeholder="硬件类型筛选"
          clearable
          @change="handleFilter"
          style="min-width: 130px;"
        >
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部类型" value="" />
          <el-option label="UART" value="uart" />
          <el-option label="I2C" value="i2c" />
          <el-option label="SPI" value="spi" />

          <el-option label="ADC" value="adc" />
        </el-select>
      </div>
    </div>

    <!-- 加载骨架 -->
    <template v-if="loading && channels.length === 0">
      <div class="skeleton-grid">
        <SkeletonCard v-for="i in 6" :key="i" variant="card" animated />
      </div>
    </template>

    <!-- 空状态 -->
    <EmptyState
      v-else-if="filteredChannels.length === 0 && !loading"
      icon="Connection"
      title="暂无通道"
      :description="searchKeyword || nodeFilter || hardwareTypeFilter ? '没有匹配的通道，请调整筛选条件' : '还没有配置任何通道，请先在节点详情中添加通道'"
    />

    <!-- 通道表格 -->
    <el-table
      v-else
      :data="paginatedChannels"
      stripe
      class="channel-table"
      @row-click="goToNodeDetail"
      v-loading="loading"
    >
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column label="节点名称" min-width="140">
        <template #default="{ row }">
          <div class="node-name-cell">
            <el-icon :size="20" :color="getNodeStatus(row.node_id) === 'online' ? 'var(--el-color-success)' : 'var(--el-text-color-secondary)'">
              <Cpu />
            </el-icon>
            <span>{{ getNodeName(row.node_id) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="硬件类型" width="110">
        <template #default="{ row }">
          <el-tag :type="getHardwareTagType(row.hardware_type)" size="small" effect="plain">
            {{ row.hardware_type?.toUpperCase() }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="hardware_id" label="硬件ID" width="120" />
      <el-table-column label="总线类型" width="100">
        <template #default="{ row }">
          {{ getBusTypeLabel(row.hardware_type) }}
        </template>
      </el-table-column>
      <el-table-column prop="address" label="地址" width="100">
        <template #default="{ row }">
          <span v-if="row.address">{{ row.address }}</span>
          <span v-else class="text-muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="启用状态" width="100" align="center">
        <template #default="{ row }">
          <el-switch
            :model-value="row.enabled"
            size="small"
            disabled
          />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="180" align="center">
        <template #default="{ row }">
          <el-button
            v-if="isScannable(row)"
            type="warning"
            text
            size="small"
            :loading="scanningId === row.id"
            @click.stop="handleScan(row)"
          >
            地址扫描
          </el-button>
          <el-button
            type="primary"
            text
            size="small"
            @click.stop="goToNodeDetail(row)"
          >
            查看节点
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 分页 -->
    <div v-if="filteredChannels.length > pageSize" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        :page-size="pageSize"
        :total="filteredChannels.length"
        layout="total, prev, pager, next"
        background
        small
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, Filter, Cpu } from '@element-plus/icons-vue'
import { channelApi, type Channel } from '@/api/channel'
import { useNodeStore } from '@/stores/node'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'

const router = useRouter()
const nodeStore = useNodeStore()

// 数据
const channels = ref<Channel[]>([])
const loading = ref(false)
const scanningId = ref<number | null>(null)

function isScannable(row: any): boolean {
  if (row.hardware_type === 'i2c') return true
  if (row.hardware_type === 'uart') {
    // bus_config may be raw hex (e.g. "1415000012C0") or JSON with mode field
    // For raw hex, treat all UART channels as potentially scannable
    // (RS485 mode cannot be determined from hex alone)
    try {
      const cfg = typeof row.bus_config === 'string' ? JSON.parse(row.bus_config) : row.bus_config
      if (cfg?.mode === 'rs485') return true
      // If JSON parse succeeded but no rs485 mode, fall through to check raw hex
    } catch {
      // bus_config is raw hex — treat as scannable (most UART deployments are RS485)
    }
    // Any UART channel with bus_config present is potentially RS485
    return !!row.bus_config
  }
  return false
}

async function handleScan(row: any) {
  scanningId.value = row.id
  try {
    const scanType = row.hardware_type === 'i2c' ? 'i2c' : 'modbus'
    const result = await channelApi.scan(row.id, { scan_type: scanType })
    ElMessage.success(`扫描完成: 发现 ${result.devices?.length || 0} 个设备`)
  } catch (e: any) {
    ElMessage.error('扫描失败: ' + (e.message || '未知错误'))
  } finally {
    scanningId.value = null
  }
}

// 筛选
const searchKeyword = ref('')
const nodeFilter = ref<number | ''>('')
const hardwareTypeFilter = ref('')

// 分页
const currentPage = ref(1)
const pageSize = 20

// 节点选项
const nodeListParams = { page: 1, page_size: 20 }
const cachedNodes = ref<any[]>([])
const nodeOptions = computed(() => cachedNodes.value)

// 节点名称映射
const nodeMap = computed(() => {
  const map = new Map<number, any>()
  for (const node of cachedNodes.value) {
    map.set(node.id, node)
  }
  return map
})

function getNodeName(nodeId: number): string {
  const node = nodeMap.value.get(nodeId)
  return node?.name || `节点 #${nodeId}`
}

function getNodeStatus(nodeId: number): string {
  const node = nodeMap.value.get(nodeId)
  return node?.status || 'unknown'
}

// 筛选后的通道列表
const filteredChannels = computed(() => {
  let result = [...channels.value]

  // 节点筛选
  if (nodeFilter.value !== '') {
    result = result.filter(ch => ch.node_id === nodeFilter.value)
  }

  // 硬件类型筛选
  if (hardwareTypeFilter.value) {
    result = result.filter(ch => ch.hardware_type === hardwareTypeFilter.value)
  }

  // 关键词搜索
  if (searchKeyword.value) {
    const keyword = searchKeyword.value.toLowerCase()
    result = result.filter(ch => {
      const name = (ch.name || '').toLowerCase()
      const hwId = (ch.hardware_id || '').toLowerCase()
      const nodeName = getNodeName(ch.node_id).toLowerCase()
      return name.includes(keyword) || hwId.includes(keyword) || nodeName.includes(keyword)
    })
  }

  return result
})

// 分页后的通道列表
const paginatedChannels = computed(() => {
  const start = (currentPage.value - 1) * pageSize
  return filteredChannels.value.slice(start, start + pageSize)
})

// 工具函数
function getHardwareTagType(type: string): '' | 'success' | 'warning' | 'info' | 'danger' {
  const map: Record<string, '' | 'success' | 'warning' | 'info' | 'danger'> = {
    uart: '',
    i2c: 'success',
    spi: 'warning',
    gpio: 'info',
    adc: 'danger',
    pwm: 'info',
  }
  return map[type] ?? 'info'
}

function getBusTypeLabel(type: string): string {
  const map: Record<string, string> = {
    uart: '串行',
    i2c: 'I²C',
    spi: 'SPI',
    gpio: '数字IO',
    adc: '模拟',
    pwm: 'PWM',
  }
  return map[type] || type
}

function getStatusLabel(status?: string): string {
  const map: Record<string, string> = {
    active: '启用',
    inactive: '停用',
    error: '错误',
  }
  return map[status || ''] || '未知'
}

// 事件处理
function handleSearch() {
  currentPage.value = 1
}

function handleFilter() {
  currentPage.value = 1
}

function goToNodeDetail(row: Channel) {
  router.push({ name: 'NodeDetail', params: { id: row.node_id } })
}

// 数据加载
async function refreshData() {
  loading.value = true
  try {
    // 加载节点列表（用于名称映射）
    await nodeStore.fetchNodes(nodeListParams)
    cachedNodes.value = nodeStore.getCachedList(nodeListParams)?.items || []
    // 加载所有通道（不限定节点）
    const res = await channelApi.getList()
    if (Array.isArray(res)) {
      channels.value = res
    } else if (res && typeof res === 'object' && 'items' in res) {
      channels.value = (res as any).items || []
    } else {
      channels.value = []
    }
  } catch (error) {
    console.error('获取通道列表失败', error)
    channels.value = []
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  refreshData()
})
</script>

<style scoped>
.channel-page {
  padding: 0;
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  gap: 12px;
}

.toolbar-left {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.search-input {
  width: 240px;
}

.skeleton-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.channel-table {
  width: 100%;
  cursor: pointer;
}

.channel-table :deep(.el-table__row) {
  cursor: pointer;
}

.channel-table :deep(.el-table__row:hover) {
  background-color: var(--el-fill-color-light);
}

.node-name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}

.text-muted {
  color: var(--el-text-color-secondary);
}

.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
</style>
