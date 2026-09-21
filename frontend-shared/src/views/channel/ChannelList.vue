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
        <!-- 范围词（规范 §4.3 MUST）：关键词是**客户端**过滤，只作用于服务端返回的当前页。
             不标注范围，用户会把"本页没搜到"当成"全库没有"——那是伪造的领域事实。 -->
        <span class="search-scope-hint" data-test="channel-search-scope">{{ searchScopeHint }}</span>

        <el-select
          v-model="nodeFilter"
          placeholder="节点筛选"
          clearable
          class="filter-select"
          @change="handleFilter"
        >
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部节点" value="" />
          <!-- :value 必须是**物理序列号** node.node_id，不是 nodes 表自增主键 node.id：
               后端按 node_id 过滤（先试 ParseUint，失败则按 nodes.node_id 查），
               而主键 number 与 ch.node_id(string) 恒不相等 —— 旧写法选中后恒返回 0 行。 -->
          <el-option
            v-for="node in nodeOptions"
            :key="node.node_id"
            :label="node.name"
            :value="node.node_id"
          />
        </el-select>

        <el-select
          v-model="hardwareTypeFilter"
          placeholder="硬件类型筛选"
          clearable
          class="filter-select"
          @change="handleFilter"
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

        <!-- 节点下拉按页长上界一次拉取；节点总数超过上界时必须**显式**提示已截断，
             否则"选不到某个节点"会变成无解释的静默截断。 -->
        <el-alert
          v-if="nodeFilterTruncated"
          type="warning"
          :closable="false"
          show-icon
          class="node-truncated-alert"
          data-test="channel-node-truncated"
          :title="nodeTruncatedText"
        />
      </div>
    </div>

    <!-- 错误态：接口失败必须留下常驻痕迹并提供重试入口，
         不得回退成"0 条 / 暂无通道"这一伪造的领域事实（范式同 views/data-source/DataSourceList.vue） -->
    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      class="channel-error-alert"
      data-test="channel-error"
    >
      <template #title>
        <span class="channel-error-text">加载通道列表失败：{{ loadError }}</span>
        <el-button link type="primary" size="small" data-test="channel-retry" @click="retryLoad">重试</el-button>
      </template>
    </el-alert>

    <!-- 加载骨架 -->
    <template v-if="loading && channels.length === 0">
      <div class="skeleton-grid">
        <SkeletonCard v-for="i in 6" :key="i" variant="card" animated />
      </div>
    </template>

    <!-- 错误状态：优先于空态。加载失败不是"没有通道"，不得用空态冒充（§3.4.2）。 -->
    <EmptyState
      v-else-if="loadError"
      kind="error"
      icon="WarningFilled"
      data-test="channel-error-state"
      title="通道列表加载失败"
      description="接口请求失败，暂时无法确定通道数量。"
      :quick-actions="[{ label: '重试', type: 'primary', handler: retryLoad }]"
    />

    <!-- 空状态：三种结论必须分开（不得互相冒充）
           ① 服务端全库确实没有通道（无筛选）    → initial
           ② 服务端筛选后全库无匹配（节点/硬件类型）→ filtered，范围=整库筛选
           ③ 只有关键词、且**本页**无匹配（全库可能还有）→ filtered，范围=本页
         ③ 与 ② 的区别是本页分页 + 客户端关键词的必然结果：关键词不下沉服务端，
         所以"本页没搜到"永远不能写成"全库没有"。 -->
    <EmptyState
      v-else-if="visibleChannels.length === 0 && !loading"
      :kind="hasActiveFilters || Boolean(searchKeyword) ? 'filtered' : 'initial'"
      icon="Connection"
      :title="emptyTitle"
      :description="emptyDescription"
    />

    <!-- 通道表格（移动端可横向滚动，见 theme.css .mobile-table-wrapper） -->
    <div v-else class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
      :data="visibleChannels"
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
      <el-table-column label="启用状态" width="110" align="center">
        <template #default="{ row }">
          <div class="status-cell">
            <!-- 这里是**只读状态指示**（disabled），但依然需要可访问名：
                 screen reader 对 disabled 开关仍会读出"开关，关/开"，
                 没有名字时用户不知道它在说哪个通道的状态。
                 真实开关在通道详情/编辑里，故文案标明"状态"。 -->
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${asChannel(row).name} 启用状态`"
              size="small"
              disabled
            />
            <span class="status-text" :class="{ off: !row.enabled }">
              {{ row.enabled ? '启用' : '禁用' }}
            </span>
          </div>
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
            @click.stop="goToNodeDetail(asChannel(row))"
          >
            查看节点
          </el-button>
        </template>
      </el-table-column>
    </el-table>
    </div>

    <!-- 分页：total 必须用**服务端** total（全库过滤后总条数），不能用本地数组长度。
         v-if 用 total > 0 而非 total > pageSize：否则只有一页时用户看不到"共 N 条"，
         而"共 N 条"必须与实际服务端条数一致（规范 §4.3.4）。 -->
    <div v-if="total > 0" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        background
        size="small"
        @current-change="handlePageChange"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { feedback } from '@/utils/feedback'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, Filter, Cpu } from '@element-plus/icons-vue'
import { channelApi, NODE_FILTER_MAX_PAGE_SIZE, type Channel } from '@/api/channel'
import { useNodeStore } from '@/stores/node'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { getHardwareTagType } from '@/utils/hardwareTag'

const router = useRouter()
const nodeStore = useNodeStore()

// 数据
const channels = ref<Channel[]>([])
const loading = ref(false)
const scanningId = ref<number | null>(null)

// 接口失败态：'' 表示无错误。失败时页面必须留下常驻痕迹（错误提示条 + 错误空态 + 重试），
// 而不是把"未知条数"渲染成 0 条 /"暂无通道"（§3.2.5 不得以本地默认值伪造事实）。
const loadError = ref('')

/** 用接口返回的 message 说明失败原因，缺省给通用文案。 */
function errorMessage(err: unknown): string {
  return err instanceof Error && err.message ? err.message : '网络请求失败'
}

/** 是否处于**服务端**筛选态（节点 / 硬件类型）—— 决定空态结论的范围是"整库筛选"。 */
const hasActiveFilters = computed(() => Boolean(nodeFilter.value || hardwareTypeFilter.value))

/**
 * 范围词（规范 §4.3 MUST，范式同 EdgeDeviceList.vue:623）。
 * 关键词搜索是**客户端**过滤，只作用于服务端返回的当前页；不标注范围，
 * 用户会把"本页没搜到"读成"全库没有"（伪造的领域事实，本仓 F14 刚修过同类）。
 */
const SCOPE_PAGE = '本页'

function isScannable(row: any): boolean {
  // 归一化：后端 hardware_type 存的是**大写**枚举（生产库实测只有 UART/I2C/SPI…），
  // 而本页历史比较写的是小写字面量 ⇒ 真实数据下恒 false（扫描按钮永不出现）。
  const hw = String(row?.hardware_type || '').toLowerCase()
  if (hw === 'i2c') return true
  if (hw === 'uart') {
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
    // 同一根因：大写数据下三元判断恒走 modbus（I2C 总线被当成 Modbus 扫描）
    const scanType = String(row?.hardware_type || '').toLowerCase() === 'i2c' ? 'i2c' : 'modbus'
    const result = await channelApi.scan(row.id, { scan_type: scanType })
    ElMessage.success(`扫描完成: 发现 ${result.devices?.length || 0} 个设备`)
  } catch (e: any) {
    feedback.handleError(e, '扫描失败')
  } finally {
    scanningId.value = null
  }
}

// 筛选
const searchKeyword = ref('')
/** 节点筛选值是**物理序列号**（node.node_id，如 'F0F5BDFFFE02'），不是 nodes 表主键。 */
const nodeFilter = ref<string | number | ''>('')
const hardwareTypeFilter = ref('')

// 分页（服务端分页：currentPage/total 与接口一一对应，不再有本地切片）
const currentPage = ref(1)
const pageSize = 20
/** 服务端返回的**过滤后全库总条数**。分页器与"共 N 条"都必须用它，不能用当前页行数。 */
const total = ref(0)

// 节点选项
//
// page_size 用**上界**而不是 20：审计库实测 418 个节点，而下拉没有翻页入口，
// 只取 20 个会让其余节点无法选择（静默截断）。超过上界的部分由 nodeFilterTruncated 显式提示。
const nodeListParams = { page: 1, page_size: NODE_FILTER_MAX_PAGE_SIZE }
const cachedNodes = ref<any[]>([])
const nodeOptions = computed(() => cachedNodes.value)
/** 服务端节点总数（用于判断下拉是否被页长上界截断）。 */
const nodeTotal = ref(0)
/** 节点下拉被页长上界截断时为 true —— 此时必须显式提示，不得静默。 */
const nodeFilterTruncated = computed(() => nodeTotal.value > NODE_FILTER_MAX_PAGE_SIZE)
const nodeTruncatedText = computed(
  () => `节点下拉已按上界截断：共 ${nodeTotal.value} 个节点，仅列出前 ${NODE_FILTER_MAX_PAGE_SIZE} 个，其余节点暂不可选。`,
)

// 节点名称映射
//
// ⚠️ 键必须是 nodes.node_id（**物理序列号**），因为 ch.node_id 是序列号字符串；
// 用主键 node.id 建映射会让所有行退化成 "节点 #<序列号>"（缺陷①的第二个表现面）。
// 同时兼容 id 建键：旧后端/mock 可能不返回 node_id，退化为主键总比整列显示不出来强。
const nodeMap = computed(() => {
  const map = new Map<string, any>()
  for (const node of cachedNodes.value) {
    if (node.node_id !== undefined && node.node_id !== null) map.set(String(node.node_id), node)
    map.set(String(node.id), node)
  }
  return map
})

function getNodeName(nodeId: number | string): string {
  const node = nodeMap.value.get(String(nodeId))
  return node?.name || `节点 #${nodeId}`
}

function getNodeStatus(nodeId: number | string): string {
  const node = nodeMap.value.get(String(nodeId))
  return node?.status || 'unknown'
}

// 服务端返回的当前页数据（节点/硬件类型筛选**已下沉服务端**，故这里不再按它们过滤）
const visibleChannels = computed(() => {
  if (!searchKeyword.value) return channels.value
  // 关键词搜索：留在客户端，且**只作用于当前页**（范围词见 searchScopeHint / 空态文案）。
  // 因此这里绝不能写"没有匹配的通道"式的全库结论。
  const keyword = searchKeyword.value.toLowerCase()
  return channels.value.filter(ch => {
    const name = (ch.name || '').toLowerCase()
    const hwId = (ch.hardware_id || '').toLowerCase()
    const nodeName = getNodeName(ch.node_id).toLowerCase()
    return name.includes(keyword) || hwId.includes(keyword) || nodeName.includes(keyword)
  })
})

/**
 * 关键词范围提示：常驻显示（不只在空态里），因为用户在下拉/输入时就该知道检索范围。
 * "本页"来自服务端分页的 page_size 与当前页——关键词既不重新请求也不跨越未加载的页。
 */
const searchScopeHint = computed(
  () => `关键词检索范围：${SCOPE_PAGE}（第 ${currentPage.value} 页，共 ${pageSize} 条/页）`,
)

/**
 * 空态标题/描述：把三种"空"分开说清——
 *   ① 服务端全库确实没有通道；
 *   ② 服务端筛选（节点/硬件类型）后全库无匹配；
 *   ③ 只有关键词、且本页无匹配（全库可能仍有匹配，因为关键词不下沉服务端）。
 * ③ 的文案**必须**带范围词"本页"，否则就是把"本页没搜到"说成"全库没有"。
 */
const emptyTitle = computed(() => {
  if (searchKeyword.value && !hasActiveFilters.value) return `${SCOPE_PAGE}没有匹配的通道`
  if (hasActiveFilters.value) return '没有匹配的通道'
  return '暂无通道'
})

const emptyDescription = computed(() => {
  if (searchKeyword.value && !hasActiveFilters.value) {
    return `关键词只检索${SCOPE_PAGE}（第 ${currentPage.value} 页）内的通道，全库可能仍有匹配。请翻页或调整关键词。`
  }
  if (hasActiveFilters.value) {
    return '按当前节点/硬件类型筛选，全库没有匹配的通道，请调整筛选条件。'
  }
  return '还没有配置任何通道，请先在节点详情中添加通道'
})

// 工具函数
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

// 事件处理
//
// 关键词仍是**客户端**过滤（不下沉服务端、不下发 search 参数），但必须**同时回到第 1 页
// 并重新取数**：在服务端分页下，currentPage 只是分页器的显示状态，它是 refreshData 的
// **输入**；程序化改绑定值**不会**触发 el-pagination 的 current-change（EP 只在分页器
// 内部 setter 里 emit，程序化更新走的是 update:current-page）。
// 所以只写 currentPage=1 会留下"页码/范围提示显示第 1 页、表格却是第 2 页数据"的
// 伪造领域事实 —— 范围词标注得越确定，这个不一致越有害。
function handleSearch() {
  currentPage.value = 1
  void refreshData()
}

/**
 * 服务端筛选变化：节点/硬件类型都已下沉到 GET /channels，
 * 因此必须**重新请求**并把页码重置回第 1 页（否则会在第 2 页请求"筛完只剩 3 条"的页，
 * 直接看到空表 —— 这正是"筛选后恒返回 0 行"的另一个表现面）。
 */
function handleFilter() {
  currentPage.value = 1
  void refreshData()
}

/** 翻页：服务端分页必须重新请求，不能本地切片。 */
function handlePageChange() {
  void refreshData()
}

function goToNodeDetail(row: Channel) {
  router.push({ name: 'NodeDetail', params: { id: row.node_id } })
}

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asChannel = (row: unknown) => row as Channel

// 数据加载
//
// 请求代际：refreshData 可能被并发触发（连点翻页、翻页途中改筛选/输入关键词）。
// 先发的请求若**后到**，会把新页数据覆盖成旧页数据 —— 又是一次"页码改了但数据没跟上"。
// 因此只接受最新一代请求的响应，过期响应一律丢弃（loading 也由最新一代负责收尾）。
let requestGeneration = 0

async function refreshData() {
  const generation = ++requestGeneration
  loading.value = true
  try {
    // 加载节点列表（用于名称映射 + 筛选下拉）。参数走页长上界，避免下拉被静默截断。
    await nodeStore.fetchNodes(nodeListParams)
    const cached = nodeStore.getCachedList(nodeListParams)
    cachedNodes.value = cached?.items || []
    nodeTotal.value = cached?.total ?? cachedNodes.value.length
    // 加载通道：**服务端分页 + 服务端筛选**（方案 C）
    //   · node_id 下发的是物理序列号字符串（后端先试 ParseUint，失败按 nodes.node_id 查）；
    //   · hardware_type 下发小写（后端 UPPER(...) 比较，大小写不敏感）；
    //   · total 用服务端回传值，页面不再本地切片。
    const res = await channelApi.getPage({
      page: currentPage.value,
      page_size: pageSize,
      node_id: nodeFilter.value === '' ? undefined : nodeFilter.value,
      hardware_type: hardwareTypeFilter.value || undefined,
    })
    if (generation !== requestGeneration) return
    channels.value = res.items
    total.value = res.total
    // 服务端 clamp 后的页码要回填，否则页码可能停在越界值上（翻页器与请求脱节）
    if (res.page !== currentPage.value) currentPage.value = res.page
    loadError.value = ''
  } catch (error) {
    if (generation !== requestGeneration) return
    // 失败必须置常驻错误态：瞬态 ElMessage 不能替代错误态（U-1 根因三件套之三）。
    loadError.value = errorMessage(error)
    feedback.handleError(error, '获取通道列表失败')
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

/** 错误态的重试入口：清空错误后重新拉取。 */
function retryLoad() {
  loadError.value = ''
  void refreshData()
}

onMounted(() => {
  refreshData()
})
</script>

<style scoped>
.channel-page {
  padding: 0;
}

.channel-error-alert {
  border-radius: 8px;
  margin-bottom: 16px;
}

.channel-error-text {
  margin-right: 8px;
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

.search-input,
.filter-select {
  width: 200px;
}

/* 关键词范围词（规范 §4.3）：常驻可见，不能被窄屏挤掉 */
.search-scope-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}

.node-truncated-alert {
  border-radius: 8px;
  margin-bottom: 12px;
}

.status-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
}

.status-text {
  font-size: 12px;
  color: var(--el-color-success);
}

.status-text.off {
  color: var(--el-text-color-secondary);
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

/* 移动端：筛选区竖排堆叠 */
@media (max-width: 768px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .toolbar-left {
    flex-direction: column;
    align-items: stretch;
  }

  .search-input,
  .filter-select {
    width: 100%;
  }

  .pagination-wrapper {
    justify-content: center;
  }
}
</style>
