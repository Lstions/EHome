import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { channelApi, NODE_FILTER_MAX_PAGE_SIZE, type Channel } from '@/api/channel'
import { useNodeStore } from '@/stores/node'

/**
 * ChannelList 页面的列表状态与取数（2026-11-12 纯搬移拆分）。
 *
 * 边界依据（为什么整段搬走、不切碎）：
 *   · 数据 / 筛选 / 分页共用同一份「页码 + 请求代际 + 服务端 total」状态，
 *     refreshData 是唯一取数入口，handleSearch / handleFilter / handlePageChange 都只是
 *     「改输入 → 重新取数」的薄壳。切开就会出现两套页码口径 —— 原脚本注释记录的正是
 *     「只改 currentPage 不发请求 ⇒ 页码/范围提示显示第 1 页、表格却是第 2 页数据」的
 *     伪造领域事实；那正是最容易在拆分中复发的一类缺陷。
 *   · 错误态（loadError）与请求代际必须在同一条 await 链上收尾，拆开会让过期响应覆盖新页。
 *   · 行内动作（isScannable / handleScan / scanningId）跟着本页数据语义走：scanningId 是
 *     模板直接绑定的行内 loading，留在同一处才不会出现两份状态。
 *
 * 留在页面的是**模板结构**：页头、工具栏、错误/空态、宽表（含 .mobile-table-wrapper 与
 * .mobile-table-hint）与分页器。listPageConventions.spec.ts / MobileTableWrapper.spec.ts /
 * PaginationSizeApi.spec.ts 三条源码门禁按「页面文件里必须有 X」定位它们，不能搬；
 * 路由跳转（goToNodeDetail）也留在页面，它是模板事件与页面的出口契约。
 */
export function useChannelList() {
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

  // 筛选
  const searchKeyword = ref('')
  /** 节点筛选值是**物理序列号**（node.node_id，如 'F0F5BDFFFE02'），不是 nodes 表主键。 */
  const nodeFilter = ref<string | number | ''>('')
  const hardwareTypeFilter = ref('')

  /**
   * 消费节点页「查看全部」的 `?node=<序列号>` 深链（NodeOverview.navigateToNodeChannels 推的）。
   *
   * 改前该 query 全仓无消费者 —— 用户在节点详情点「查看全部」，落到通道列表后
   * 筛选仍是「全部节点」，看到的是**全站**通道而不是刚看的那个节点的，
   * 且 URL 里的 node 参数与页面状态不一致（看起来像点了没反应/落错页）。
   * 这里与既有 route.query.search 的初始化写法保持一致：初始化即生效，不弹提示。
   */
  const route = useRoute()
  const routeNode = typeof route.query.node === 'string' ? route.query.node : ''
  if (routeNode) nodeFilter.value = routeNode

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
    () => `关键词检索范围：${SCOPE_PAGE}（第 ${currentPage.value} 页，每页 ${pageSize} 条）`,
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

  // 页面与测试消费的面：状态 + 动作。内部实现细节（请求代际、缓存键参数）不外露，
  // 避免模板绕开 refreshData 直接改页码/筛选后再各自取数，重新出现两套口径。
  return {
    // 数据与状态
    channels, loading, scanningId, loadError,
    // 筛选
    searchKeyword, nodeFilter, hardwareTypeFilter,
    nodeOptions, nodeFilterTruncated, nodeTruncatedText,
    hasActiveFilters,
    // 分页
    currentPage, pageSize, total,
    // 派生视图
    visibleChannels, searchScopeHint, emptyTitle, emptyDescription,
    getNodeName, getNodeStatus,
    // 行内动作
    isScannable, handleScan,
    // 取数
    refreshData, retryLoad, handleSearch, handleFilter, handlePageChange,
  }
}
