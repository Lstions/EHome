import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import { nodeDisplayName } from '@/utils/nodeDisplay'
import { useNodeStore } from '@/stores/node'
import { nodeApi, type NodeListParams } from '@/api/node'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'

/**
 * NodeList 页面的列表状态与动作（2026-11-12 纯搬移拆分）。
 *
 * 边界依据（为什么这些逻辑整段搬走、不切碎）：
 *   · 它们共用同一份「查询条件 + 请求序号 + 缓存探测」状态（currentPage/pageSize/
 *     三个筛选 ref/防抖副本/listRequestSequence），切开就会重新出现两套口径 ——
 *     NodeList.vue 原 :375-386 记录的正是"两个互不相干的 searchKeyword ref"踩过的坑；
 *   · 取数、WS 就地更新、统计重算、删除后的本地剔除必须落在同一条 await 链上，
 *     否则 sequence 守卫与 loading 收尾会错位。
 *
 * 留在页面的是**模板结构**：骨架屏、统计卡、工具栏、筛选标签、分页器、两个对话框
 * （listPageConventions / DashboardSkeletonGeometry / FillContrastContract 等源码门禁
 *  按"页面文件里必须有 X"定位它们）。子组件只做展示与事件上抛，不持有列表状态。
 */
export function useNodeList() {
  const router = useRouter()
  const route = useRoute()
  const nodeStore = useNodeStore()
  const wsStore = useWebSocketStore()

  // 状态
  const currentPage = ref(1)
  const pageSize = ref(20)
  const total = ref(0)
  const viewMode = ref<'grid' | 'list'>('grid')
  const searchKeyword = ref('')
  const statusFilter = ref('')
  const modelFilter = ref('')

  const routeSearch = typeof route.query.search === 'string' ? route.query.search : ''
  const routeStatus = typeof route.query.status === 'string' ? route.query.status : ''
  if (routeSearch) searchKeyword.value = routeSearch
  if (routeStatus === 'online' || routeStatus === 'offline') statusFilter.value = routeStatus

  // 「添加节点」深链：?action=add 直接打开对话框。
  // 改前工具栏按钮 push 的正是这个 query, 但全仓无消费者 (有按钮、没落点/假跳转) ——
  // 现在本页自己消费它, 外部深链与页内两处入口共用同一个对话框。
  const addDialogVisible = ref(route.query.action === 'add')

  const nodes = ref<any[]>([])

  // ── 服务端分页 + 服务端筛选 (负债 I-11) ──
  // 三个筛选 (search/status/model) 全部下沉到 GET /nodes, 与后端 Count/Find 两侧
  // 同口径 —— 检索覆盖全库, 而不是"当前页本地过滤伪装成全局检索" (§3.3.5)。
  // status/model 是离散选择, 变化即时下发; search 需要 300ms 防抖 (每次按键都打
  // 请求不可接受)。
  //
  // 为什么这里**不用** useDebouncedSearch: 该 composable 内部自建 searchKeyword ref,
  // 而本页已有一个模板绑定的 searchKeyword (路由 query 初始化 + 筛选标签都依赖它)。
  // 直接引入会出现**两个互不相干的 ref** —— 输入框写的是页面那个, debouncedKeyword
  // 跟随的是 composable 那个(永远为空), 结果是"搜索框能打字、请求里却没有 search",
  // 且在单测里表现为"筛选项看起来生效、实际没下发"。故此处按同一语义 (清空立即生效、
  // 非空延迟 300ms) 直接对页面自己的 ref 做防抖。
  const debouncedSearch = ref('')
  let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null
  watch(searchKeyword, (val) => {
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
    if (!val) {
      // 清空立即生效: 用户点"×"时期待立刻看到全量, 不是 300ms 后。
      debouncedSearch.value = ''
      return
    }
    searchDebounceTimer = setTimeout(() => {
      searchDebounceTimer = null
      debouncedSearch.value = val
    }, 300)
  })

  const getListParams = (): NodeListParams => {
    const params: NodeListParams = { page: currentPage.value, page_size: pageSize.value }
    if (debouncedSearch.value.trim()) params.search = debouncedSearch.value.trim()
    if (statusFilter.value) params.status = statusFilter.value
    if (modelFilter.value) params.model = modelFilter.value
    return params
  }
  // 首屏缓存读取必须在 nodes 声明之后 —— 它现在要拿 getListParams() 的结果。
  const initialCache = nodeStore.getCachedList(getListParams())
  const hasInitialCache = !!initialCache
  const loading = ref(!hasInitialCache)
  const refreshing = ref(false)
  nodes.value = initialCache?.items || []

  const hasActiveFilters = computed(() => Boolean(searchKeyword.value || statusFilter.value || modelFilter.value))

  // 统计数据
  const stats = reactive({
    total: 0,
    online: 0,
    offline: 0,
    warning: 0,
  })

  // 型号选项
  const modelOptions = computed(() => {
    const models = new Set(nodes.value.map(c => c.model).filter(Boolean))
    return Array.from(models)
  })

  // 表格/卡片直接渲染接口返回的当前页, **不再做本地切片/过滤** (真分页)。
  // 改前这里对 nodes.value (当时是全量数组) 做本地 filter, 于是"筛选"只在当前页
  // 生效、"翻页"只是本地切片 —— 两者都伪装成了全局行为。筛选已下沉服务端,
  // 本地再过滤一次只会把服务端已经筛好的结果二次筛一遍 (且有口径漂移风险:
  // 服务端 search 大小写不敏感、model 精确匹配, 本地实现必须与之一致)。
  const filteredNodes = computed(() => nodes.value)

  // 更新统计
  const updateStats = () => {
    const list = nodes.value
    stats.total = list.length
    stats.online = list.filter(c => c.status === 'online').length
    stats.offline = list.filter(c => c.status === 'offline').length
    stats.warning = list.filter(c => c.status === 'warning').length
  }

  // 获取节点列表
  // silent=true: 不显示骨架屏（用于 WS 推送的静默刷新）
  let listRequestSequence = 0
  const fetchNodes = async (silent = false, force = false, throwOnError = false) => {
    const sequence = ++listRequestSequence
    const params = getListParams()
    const showInitialSkeleton = !silent && !nodeStore.hasCachedList(params)
    if (showInitialSkeleton) loading.value = true
    try {
      await nodeStore.fetchNodes(params, force)
      if (sequence !== listRequestSequence) return
      const cached = nodeStore.getCachedList(params)
      nodes.value = cached?.items || []
      total.value = cached?.total || 0
      updateStats()
    } catch (error: any) {
      if (sequence === listRequestSequence) feedback.handleError(error, '获取节点列表失败')
      if (throwOnError) throw error
    } finally {
      if (showInitialSkeleton && sequence === listRequestSequence) loading.value = false
    }
  }

  // WS 推送防抖：短时间内多条事件只触发一次静默刷新
  let wsRefreshTimer: ReturnType<typeof setTimeout> | null = null
  const debouncedSilentRefresh = () => {
    if (wsRefreshTimer) clearTimeout(wsRefreshTimer)
    wsRefreshTimer = setTimeout(() => {
      wsRefreshTimer = null
      fetchNodes(true)
    }, 500)
  }

  // 刷新数据
  const refreshData = async () => {
    refreshing.value = true
    try {
      await fetchNodes(false, true, true)
      ElMessage.success('数据已刷新')
    } catch {
      // fetchNodes already displayed the request error.
    } finally {
      refreshing.value = false
    }
  }

  // 搜索和筛选
  //
  // 规范 §3.2.6 MUST: 「对会改变查询范围的输入 (节点、设备、型号、时间范围、筛选)
  // 重置或失效其派生状态: 选中项、**分页**、异步结果…」。
  // 改前 handleFilter 只把 currentPage 归 1, 但**从不重新请求** —— 页码重置了,
  // 数据还是旧的; 服务端分页后"重置页码 + 重新查询"必须成对出现, 否则用户在
  // 第 3 页切换筛选会看到第 3 页的旧数据配第 1 页的页码。
  const handleSearch = () => {
    // 仅占位: 真正的检索由上面 watch(searchKeyword) 的防抖副本触发 (300ms)。
  }

  // 每页条数变化: 页码回到第 1 页 (原页在新页长下可能已越界), 再查询。
  const handlePageSizeChange = () => {
    currentPage.value = 1
    void fetchNodes()
  }

  // 清空筛选: 只改 ref, 由下面的 watch 统一重置页码 + 重新查询 ——
  // 不在这里再调一次 fetchNodes, 否则 watch 会补发第二个请求 (重复取数)。
  // useDebouncedSearch 在清空时**立即**同步 debouncedKeyword (不走 300ms 防抖),
  // 因此三个 ref 的变化在同一 tick 内被 Vue 批处理成一次 watch 回调。
  const clearFilters = () => {
    searchKeyword.value = ''
    statusFilter.value = ''
    modelFilter.value = ''
  }

  const handleStatClick = (status: string) => {
    statusFilter.value = status === 'all' ? '' : status
  }

  // 筛选/搜索变化 → 重置页码后重新查询 (§3.2.6 MUST)。
  // 这是"筛选变化"的唯一入口: 模板里的 @change / 标签关闭 / KPI 钻取都只改 ref,
  // 由这里统一收口 —— 避免"某条路径忘了重置页码"或"重复发请求"两种偏差。
  // 注意改前的缺陷: 旧 handleFilter 把 currentPage 归 1 却**从不重新请求**,
  // 于是页码重置了、数据仍是旧的; 服务端分页后重置与重查必须成对出现。
  //
  // filterReady 门控挂载期初值: 路由 query 初始化的 search/status 已经在
  // getListParams() 首次取数时带上, 不需要再触发一次请求。
  let filterReady = false
  watch([debouncedSearch, statusFilter, modelFilter], () => {
    if (!filterReady) return
    currentPage.value = 1
    void fetchNodes()
  })

  // 跳转详情（新版节点总览页）
  const goToDetail = (nodeId: string) => {
    router.push(`/node/${nodeId}`)
  }

  // ── 添加节点（手动注册, POST /api/v1/nodes）────────────────────────────────
  // 本页此前**没有**任何创建入口: 工具栏按钮 push('/node?action=add')、空态快捷
  // 动作只弹 ElMessage.info('跳转添加页面') —— 两处都是"有按钮、没落点"。
  // 后端 POST /api/v1/nodes 已存在 (docs/设计/节点.md 明确其用途是手动注册)。
  const addForm = reactive({ node_id: '', name: '' })
  const addSubmitting = ref(false)
  /** 12 位十六进制 MAC —— 与后端 node_id 唯一键的口径一致。 */
  const NODE_ID_PATTERN = /^[0-9A-F]{12}$/

  const openAddDialog = () => {
    addDialogVisible.value = true
  }

  const resetAddForm = () => {
    addForm.node_id = ''
    addForm.name = ''
  }

  const submitAddNode = async () => {
    if (addSubmitting.value) return
    // 本地先校验、非法格式**不发请求**: 让用户立刻看到格式要求, 而不是拿到一个
    // 语焉不详的 400 "添加失败"(看不出到底哪里错了)。
    const nodeId = addForm.node_id.trim().toUpperCase()
    if (!NODE_ID_PATTERN.test(nodeId)) {
      ElMessage.warning('设备 ID 必须是 12 位十六进制，例：F0F5BD02F35C')
      return
    }
    const name = addForm.name.trim()
    addSubmitting.value = true
    try {
      await nodeApi.create(name ? { node_id: nodeId, name } : { node_id: nodeId })
      ElMessage.success('节点已添加')
      addDialogVisible.value = false
      await refreshData()
    } catch (error: any) {
      // 409 的 message 是英文 'node_id already exists', 原样透传用户看不懂。
      if (error?.status === 409) feedback.error('该 node_id 已存在')
      else feedback.handleError(error, '添加节点失败')
    } finally {
      addSubmitting.value = false
    }
  }

  // ── OTA 升级对话框（复用生产组件 OTAForm）────────────────────────────────
  const otaDialogVisible = ref(false)
  const otaNode = ref<any>({})

  const handleOTASuccess = () => {
    ElMessage.success('OTA 升级已完成')
    otaDialogVisible.value = false
    void fetchNodes(false, true)
  }

  // ── 快捷操作 ──────────────────────────────────────────────────────────────
  const handleQuickAction = (action: string, node: any) => {
    if (action === 'config') {
      router.push(`/node/${node.node_id}?tab=config`)
    } else if (action === 'ota') {
      // 改前这里 push('/firmware?node=X'), 而 FirmwareManage.vue 0 命中
      // useRoute/route.query —— 该 query 无人消费, "升级"是死入口。
      // 这里直接复用生产组件 OTAForm 开升级对话框 (与 NodeOverview 同一契约),
      // 因此 FirmwareManage 无需改动: 生产者不再产生 /firmware?node=。
      otaNode.value = node
      otaDialogVisible.value = true
    }
  }

  // 删除
  const handleDelete = async (row: any) => {
    let deleted = false
    try {
      const confirmed = await feedback.confirmDanger(
        `确定要删除节点 "${nodeDisplayName(row)}" 吗？此操作不可恢复。`,
        { title: '警告', confirmText: '删除', cancelText: '取消' }
      )
      if (!confirmed) return

      await nodeStore.deleteNode(row.id)
      deleted = true
      nodes.value = nodes.value.filter(node => node.id !== row.id && node.node_id !== String(row.id))
      total.value = Math.max(0, total.value - 1)
      updateStats()
      ElMessage.success('删除成功')
      try {
        await fetchNodes(false, true, true)
      } catch {
        ElMessage.warning('节点已删除，但列表刷新失败，请稍后刷新')
      }
    } catch (error: any) {
      if (error !== 'cancel' && !deleted) {
        feedback.handleErrorWithContext(error, '删除节点失败')
      }
    }
  }

  // WebSocket 订阅
  let unsubscribe: (() => void) | null = null

  onMounted(() => {
    fetchNodes().then(() => {
      // 首次加载后再武装筛选 watch, 避免挂载期初值触发一次多余请求。
      filterReady = true
    })
    
    // 订阅状态更新：就地更新节点状态，避免全量重拉导致屏闪
    unsubscribe = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
      const payload = message.payload
      if (!payload?.node_id) return

      // 就地更新：在已有 nodes 数组中找到对应节点并更新状态字段
      const node = nodes.value.find(n => n.node_id === payload.node_id)
      if (node) {
        // 状态未变则不做任何操作
        if (payload.status && node.status !== payload.status) {
          node.status = payload.status
          if (payload.status === 'offline') {
            node.connection_quality = 0
          }
          updateStats()
        }
      } else {
        // 新节点（列表中不存在）：防抖静默拉取
        debouncedSilentRefresh()
      }
    })
  })

  onUnmounted(() => {
    if (unsubscribe) unsubscribe()
    if (wsRefreshTimer) clearTimeout(wsRefreshTimer)
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  })

  // 页面与测试消费的面：状态 + 动作。内部实现细节（防抖副本、请求序号、缓存探测、
  // 统计重算）不外露 —— 避免模板绕开「筛选变化唯一入口」直接改内部状态。
  return {
    // 分页 / 视图 / 筛选
    currentPage, pageSize, total, viewMode,
    searchKeyword, statusFilter, modelFilter,
    hasActiveFilters, modelOptions, filteredNodes, stats,
    loading, refreshing,
    // 取数与筛选
    fetchNodes, refreshData,
    handleSearch, handlePageSizeChange, clearFilters, handleStatClick,
    // 添加节点对话框
    addDialogVisible, addForm, addSubmitting,
    openAddDialog, resetAddForm, submitAddNode,
    // OTA 对话框
    otaDialogVisible, otaNode, handleOTASuccess,
    // 行内动作与路由
    handleQuickAction, handleDelete, goToDetail,
  }
}
