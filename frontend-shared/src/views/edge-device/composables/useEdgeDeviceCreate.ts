/**
 * 边缘设备「创建 / 编辑」对话框的状态与提交事务（2026-11-12 第二轮拆分，纯搬移）。
 *
 * 边界依据（为什么是这一整块）：
 *   · 搬过来的是**一条完整的创建/编辑链路**：三步向导的表单状态（型号 / 节点&通道 /
 *     基本信息）、地址能力判定（resolveModelRequiresTargetAddress + resolveInlineDeviceAddress）、
 *     提交事务（handleCreate，含 inline 建通道 / 已有通道 / 编辑 update 三条分支），
 *     以及它们依赖的数据加载（配置模板 / 节点能力 / 驱动树 / 逐指令轮询间隔）。
 *   · **没有**搬过来的是页面口径：统计卡、筛选、分页、批量条、卡片展示映射仍在
 *     EdgeDeviceList.vue；showCreateDialog 也由页面持有（工具栏按钮要直接置 true），
 *     这里只通过 deps 读写它，避免出现第二份开关状态。
 *   · 状态仍是「每个页面实例一份」：所有 ref / reactive 都在函数体内创建，
 *     返回的是同一批对象，页面解构后与子组件对话框读写同一份状态（不是副本）。
 */
import { computed, onUnmounted, reactive, ref, watch, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useChannelStore } from '@/stores/channel'
import { useNodeStore } from '@/stores/node'
import { useParserStore } from '@/stores/parser'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import { nodeApi } from '@/api/node'
import { logger } from '@/utils/logger'
import { findDriverLeaf, getDriverTree, type DriverTreeNode } from '@/api/driver'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'
import { feedback } from '@/utils/feedback'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { DEVICE_ADDRESS_DEFAULT, isValidDeviceAddress, parseDeviceAddress } from '@/utils/deviceAddress'

/** 创建 / 编辑向导需要的页面侧依赖：组合式不持有它们，避免第二份状态。 */
export interface EdgeDeviceCreateDeps {
  /** 对话框开关由页面持有（工具栏「创建边缘设备」直接置 true），组合式只读写同一个 ref。 */
  showCreateDialog: Ref<boolean>
  /**
   * 列表刷新。页面里的 fetchDevices 在本组合式**之后**声明，故只能传惰性适配器：
   * setup 期不会调用它，只有 handleCreate 在 await 之后才用（与 useEdgeDeviceBatch 同一约定）。
   */
  fetchDevices: (force?: boolean, throwOnError?: boolean) => Promise<void>
}

/** 组合式返回的控制器（全部状态 / 派生值 / 动作）。 */
export type EdgeDeviceCreateCtl = ReturnType<typeof useEdgeDeviceCreate>
/** 页面额外塞进控制器的展示口径（不属于表单/事务，故不进组合式）。 */
export interface EdgeDeviceCreateDialogExtra {
  /** 通道选择卡上的通道名：页面与卡片/表格共用 getHardwareDisplay 这一份事实源。 */
  channelCardLabel: (ch: Channel) => string
}
/** EdgeDeviceCreateDialog.vue 的 ctl 入参类型。 */
export type EdgeDeviceCreateDialogCtl = EdgeDeviceCreateCtl & EdgeDeviceCreateDialogExtra

export function useEdgeDeviceCreate(deps: EdgeDeviceCreateDeps) {
  const { showCreateDialog, fetchDevices } = deps
  const nodeStore = useNodeStore()
  const channelStore = useChannelStore()
  const parserStore = useParserStore()
  const edgeDeviceStore = useEdgeDeviceStore()

  // 提交中标志 / 「创建事务代号」：关闭对话框或组件卸载都会自增代号，
  // 让仍在飞行中的 create / update 在下一个 await 之后作废（见 handleCreate 的两道检查）。
  const submitting = ref(false)
  let createTransactionGeneration = 0

  // 节点列表：只被创建向导的「所属节点」下拉消费（原页面 state 块里的第一项）。
  const collectors = ref<any[]>([])

  // ── m-1（2026-09-22）：型号「是否消费设备地址」的能力来源 ─────────────
  // 唯一数据源就是 /device-configs/tree 的驱动叶子字段 requires_target_address
  // （与后端地址门禁 driverRequiresTargetAddress 同一口径）。这里**不新造**第二个
  // 数据源，也不在本文件重写一份判定表：Tree → 型号 → 能力，全程同一张快照。
  //
  // 为什么能力必须来自后端而不是"字符串形态"：后端只对**真的解析地址**的型号
  // （纯 Modbus 类）校验 1-254；纯 I2C/SPI 采集型号原样接受任何标识。按形态判断
  // 就会给后者凭空写一个它根本不用的地址 1 —— 正是本次要修的口径不一致。
  const driverTree = ref<DriverTreeNode[]>([])
  const driverTreeLoaded = ref(false)

  /**
   * 该型号是否消费设备地址。三分支（**必须**保持可区分，不能压成布尔）：
   *   true      → 要求地址；
   *   false     → 明确不要求；
   *   undefined → 拿不到能力字段（老后端 / 树未加载完 / 该型号只在 DB 模板里）
   *               ⇒ 调用方退回"字段缺失"的兼容行为。
   *
   * findDriverLeaf 是 @/api/driver 暴露的唯一查询入口，避免各处 find 出不同口径。
   */
  const resolveModelRequiresTargetAddress = (deviceType: string | undefined): boolean | undefined => {
    const leaf = findDriverLeaf(driverTree.value, String(deviceType ?? '').trim())
    return typeof leaf?.requires_target_address === 'boolean' ? leaf.requires_target_address : undefined
  }

  // 向导状态
  const createStep = ref(0)
  const selectedParser = ref<Parser | null>(null)
  const selectedChannel = ref<Channel | null>(null)
  const channelTab = ref('existing')

  // 模板选择 (Step 0 可选)
  const availableTemplates = ref<DeviceConfig[]>([])
  const selectedTemplateId = ref<number | null>(null)
  const selectedTemplate = computed(() =>
    availableTemplates.value.find((t) => t.id === selectedTemplateId.value) || null,
  )

  // 加载配置模板 (Step 0)
  const loadTemplates = async () => {
    try {
      const res = await deviceConfigApi.getList({ page_size: 100 })
      availableTemplates.value = res.items || []
    } catch (error) {
      availableTemplates.value = []
      throw error
    }
  }

  // 选模板后: 预填表单 + 尝试自动选解析器
  const onTemplateSelected = async (templateId: number | undefined | null) => {
    if (!templateId) {
      return
    }
    const tpl = availableTemplates.value.find((t) => t.id === templateId)
    if (!tpl) return
    // 预填设备类型 / 硬件类型 / 协议
    deviceForm.device_type = tpl.device_type
    deviceForm.hardware_type = (tpl.hardware_type as any) || 'uart'
    deviceForm.protocol = (tpl.protocol as any) || 'modbus'
    // 尝试自动选解析器 (用 parser_id 匹配)
    if (tpl.parser_id && availableParsers.value?.length > 0) {
      const matched = availableParsers.value.find(
        (p: Parser) => p.id === tpl.parser_id || p.id?.endsWith(tpl.parser_id || ''),
      )
      if (matched) {
        selectParser(matched)
      }
    }
  }

  // 编辑边缘设备ID
  const editingDeviceId = ref<number | null>(null)
  // G4 编辑-保存回路：记录"打开编辑框那一刻"的设备地址值。
  // 保存时只在**值真的变了**才把 hardware_id 发出去 —— 编辑表单当前没有任何
  // 地址输入项（见步骤 3 表单：只有名称/采集间隔），所以该值永远不会变，
  // 保存就不会产生任何地址写入（正是契约要求的「打开编辑框保存不应产生写入」）。
  // 若将来给编辑表单加了地址输入框，这里的比较会自动变成"改了才写"，无需再改保存逻辑。
  const editingDeviceAddressSnapshot = ref('')

  // 设备表单
  const deviceForm = reactive({
    node_id: null as number | null,
    // 编辑模式的通道绑定（契约 §4.1：与 node_id 一起提交，后端校验归属关系）。
    // 创建模式仍走向导的 selectedChannel/newChannel，不用这个字段。
    channel_id: null as number | null,
    name: '',
    device_type: '',
    protocol: 'modbus',
    hardware_type: 'uart',
    hardware_id: '',
    interval_ms: 1000
  })

  const deviceRules = {
    name: [{ required: true, message: '请输入边缘设备名称', trigger: 'blur' }],
    // S10 fix: Add node_id required rule so the red asterisk shows on the form item
    node_id: [{ required: true, message: '请选择所属节点', trigger: 'change' }],
    // 编辑模式必须同时给出通道：后端要求「新 channel 属于新 node」，
    // 只带 node_id 会被 400 拒绝（channel does not belong to node）。
    channel_id: [{ required: true, message: '请选择通道', trigger: 'change' }],
  }

  // 新通道表单
  const newChannel = reactive({
    hardware_type: 'i2c',
    hardware_id: '',
    address: '',
    spi_cs: 10,
    interval_ms: 1000
  })

  // @ts-ignore - form ref used in template
  const channelFormRef = ref()
  const deviceFormRef = ref()

  // 只显示在线节点
  const onlineCollectors = computed(() => collectors.value.filter((c: any) => c.status === 'online'))

  // 获取节点列表
  const fetchNodes = async () => {
    const params = { page: 1, page_size: 100 }
    await nodeStore.fetchNodes(params)
    collectors.value = nodeStore.getCachedList(params)?.items || []
  }

  // EDGE-WIZ-003: 打开创建向导时必须强制刷新通道列表。旧缓存 (节点页刚创建通道
  // 后不刷新浏览器) 会导致新建通道不进列表。通道 store 的 fetchChannels 自带
  // requestSequence/cacheEpoch 防竞态, 不破坏其 sessionGeneration 机制。
  let wizardDataLoaded = false
  let channelsRefreshing = false
  let nodesFetched = false
  const refreshWizardChannels = async () => {
    channelsRefreshing = true
    try {
      await channelStore.fetchChannels(undefined, true)
    } finally {
      channelsRefreshing = false
    }
  }
  /**
   * 拉取一次驱动树快照（m-1）。失败时保持 driverTreeLoaded=false，并在下次打开
   * 向导时重试 —— 不缓存"失败"这种状态，否则一次网络抖动会让能力查询在整个
   * 页面生命周期内都拿不到值。
   */
  const fetchDriverTreeOnce = async () => {
    if (driverTreeLoaded.value) return
    try {
      driverTree.value = await getDriverTree()
      driverTreeLoaded.value = true
    } catch (error) {
      logger.warn('获取驱动树失败，型号地址能力未知，退回默认地址兜底', { error: String(error) })
    }
  }

  const loadCreateWizardData = async () => {
    if (wizardDataLoaded) {
      // 向导已初始化过: 树上一次没拿到（driverTreeLoaded=false）也要补一次，
      // 否则用户第二次打开向导时仍然拿不到能力字段。
      void fetchDriverTreeOnce()
      // 向导已初始化过: 节点/型号/模板不再重复拉取, 但通道列表每次打开都强制
      // 刷新 — 节点页刚创建的通道必须立即可见。并发守卫在 store 内
      // (requestSequence), 此处只需防重复请求压栈。
      void refreshWizardChannels().catch(() => {
        // 失败保留已有列表, 用户可通过切换节点或重开向导重试
      })
      return
    }
    try {
      await Promise.all([
        (async () => {
          // 节点列表只需要拉取一次即可; 通道列表每次打开向导都强制刷新
          if (nodesFetched) return
          await fetchNodes()
          nodesFetched = true
        })(),
        refreshWizardChannels(),
        parserStore.parsers.length === 0 ? parserStore.fetchParsers(true) : Promise.resolve(),
        loadTemplates(),
        // m-1：型号能力（requires_target_address）与向导数据一起取一次。
        // 失败**不**把向导整体判为加载失败（型号列表仍可用），只让能力查询
        // 落回 undefined ⇒ 走"字段缺失"的兼容分支（默认地址 1 + 提示）。
        fetchDriverTreeOnce(),
      ])
      wizardDataLoaded = true
    } catch (error) {
      wizardDataLoaded = false
      feedback.handleError(error, '创建向导数据加载失败，请重试')
    }
  }

  // EDGE-WIZ-003: 切换节点时也保证通道列表可见。fetchChannels 内部有
  // requestSequence 防竞态: 并发请求只由最新序号写回, 不会互相覆盖。
  const handleNodeChange = () => {
    selectedChannel.value = null
    // 设备上报的总线资源是「硬件ID」下拉的唯一真源：切换节点必须按新节点重新取。
    void loadNodeCapabilities(deviceForm.node_id)
    if (wizardDataLoaded && !channelsRefreshing) {
      void channelStore.fetchChannels(undefined, true).catch(() => {
        // 失败时保留已有列表, 用户仍可重试 (切回再切或重开向导)
      })
    }
  }

  /**
   * 向导用的节点能力缓存（node_id → buses）。
   *
   * 「硬件ID」下拉必须来自**设备真实上报**的总线资源（见下方 availableBusesForType），
   * 因此打开向导/切换节点时按需拉取一次 GET /nodes/:id/capabilities。
   * 拉取失败时**不伪造资源**：下拉退回「该节点已有通道的 hardware_id」，仍无则显示未上报提示。
   */
  const nodeCapabilitiesByNode = ref<Record<string, Record<string, any>>>({})
  const capabilitiesLoading = ref(false)
  const capabilitiesRequestSequence = ref(0)
  const loadNodeCapabilities = async (nodeId: string | number | null | undefined) => {
    const key = String(nodeId ?? '')
    if (!key || nodeCapabilitiesByNode.value[key] || capabilitiesLoading.value) return
    const sequence = ++capabilitiesRequestSequence.value
    capabilitiesLoading.value = true
    try {
      const caps = await nodeApi.getCapabilities(key)
      if (sequence !== capabilitiesRequestSequence.value) return
      const buses = (caps as any)?.buses
      nodeCapabilitiesByNode.value = {
        ...nodeCapabilitiesByNode.value,
        [key]: buses && typeof buses === 'object' ? buses : {},
      }
    } catch (error) {
      // 能力上报读不到不是致命错误：下拉会退化为该节点已有通道，绝不伪造资源名。
      logger.warn('获取节点能力失败，硬件资源下拉回退为已有通道', { error: String(error) })
    } finally {
      if (sequence === capabilitiesRequestSequence.value) capabilitiesLoading.value = false
    }
  }

  /**
   * 该节点在**能力上报**里声明的总线资源 id（唯一真源）。
   *
   * 为什么必须读能力上报而不是硬编码：通道的 hardware_id 是**总线资源标识**
   * （如 UART0 / SPI2），设备实际只有它上报的那几条。改前这里在
   * 「该节点还没有此类通道」时返回硬编码的 ['UART1','UART2'] / ['I2C0','I2C1'] /
   * ['SPI1','SPI2']，既不读 capabilities、又是 1 基编号 —— 实测本机节点上报的是
   * uart=[UART0,UART1]、i2c=[I2C0]、spi=[SPI2]，于是下拉里出现设备上根本不存在的
   * UART2 / I2C1 / SPI1；用户一旦选中就会创建一个指向不存在总线的通道（静默的错误数据）。
   */
  const busResourcesForType = (hardwareType: string): string[] => {
    const nodeKey = String(deviceForm.node_id ?? '')
    const buses = nodeCapabilitiesByNode.value[nodeKey]
    if (!buses) return []
    const list = (buses as Record<string, unknown>)[hardwareType.toLowerCase()]
    if (!Array.isArray(list)) return []
    return list
      .map((resource: any) => String(resource?.id ?? '').trim())
      .filter(Boolean)
  }

  /** 该节点是否**上报过**这个总线类型（用于区分「未上报」与「上报了 0 条」）。 */
  const busTypeReported = (hardwareType: string): boolean => {
    const nodeKey = String(deviceForm.node_id ?? '')
    const buses = nodeCapabilitiesByNode.value[nodeKey]
    if (!buses) return false
    return Array.isArray((buses as Record<string, unknown>)[hardwareType.toLowerCase()])
  }

  /** 「硬件ID」下拉是否有可选项（无 ⇒ 必须提示未上报并禁用「下一步」）。 */
  const hasBusOptions = computed(() => availableBusesForType(newChannel.hardware_type).length > 0)
  /** 未上报提示文案（下拉空态与步骤校验共用，措辞只有一处）。 */
  const unavailableBusMessage = computed(() =>
    `该节点未上报 ${getHardwareLabel(newChannel.hardware_type || 'BUS')} 资源，无法在此创建该总线的通道`)

  /**
   * 向导「硬件ID」下拉的选项：通道 hardware_id 是**总线资源标识**语义，取值优先级：
   *   1) 该节点能力上报的资源 id（与设备一致，0 基）
   *   2) 该节点已有通道的 hardware_id（兼容能力上报缺失的老后端，但至少是真实存在的）
   * 两者都拿不到 ⇒ 空数组，由 UI 明确提示「未上报」并禁用下一步。
   * **绝不**回退到硬编码资源名 —— 那是把不存在的资源伪装成可选。
   */
  const availableBusesForType = (hardwareType: string): string[] => {
    if (!deviceForm.node_id) return []
    const fromCapabilities = busResourcesForType(hardwareType)
    if (fromCapabilities.length > 0) return fromCapabilities
    // P0-2: node_id / hardware_type 比较归一化——channel.node_id 可能是
    // string(设备序列号如 'F0F5BDFFFE02')也可能是 number;channel.hardware_type
    // 后端存大写 'UART',表单是小写 'uart'。两者都需宽松匹配,否则 filter 空导致
    // 硬件ID下拉无选项。
    const nodeIdStr = String(deviceForm.node_id)
    const hwTypeLower = hardwareType.toLowerCase()
    const collectorChannels = channelStore.channels.filter(
      ch => ch && String(ch.node_id) === nodeIdStr && (ch.hardware_type || '').toLowerCase() === hwTypeLower
    )
    // 提取已有的 hardware_id（如 I2C0, UART0），去重
    return [...new Set(collectorChannels.map(ch => ch.hardware_id))].filter(Boolean) as string[]
  }

  // 解析器可选列表
  const availableParsers = computed(() => {
    return parserStore.parsers.filter(p => p.hardware_types?.length > 0)
  })

  // 根据已选解析器，筛选可用的总线类型
  const selectedParserBusTypes = computed(() => {
    return selectedParser.value?.hardware_types || ['i2c', 'spi', 'uart']
  })

  // 已有通道列表（按节点和解析器总线类型过滤）
  const existingChannels = computed(() => {
    if (!selectedParser.value) return []
    const nodeIdStr = String(deviceForm.node_id ?? '')
    const parserBuses = selectedParser.value!.hardware_types.map(b => b.toLowerCase())
    return channelStore.channels.filter(ch =>
      ch && String(ch.node_id) === nodeIdStr &&
      parserBuses.includes((ch.hardware_type || '').toLowerCase())
    )
  })

  // 自动生成的通道名称（预览用）
  const generatedChannelName = computed(() => {
    if (newChannel.hardware_type === 'i2c' && newChannel.address) {
      return `${newChannel.hardware_id}_${newChannel.address}`
    }
    if (newChannel.hardware_type === 'spi') {
      return `${newChannel.hardware_id}_CS${newChannel.spi_cs}`
    }
    return newChannel.hardware_id
  })

  // 是否可以进入下一步
  const canGoNext = computed(() => {
    // 三步向导（2026-10-23 逻辑设备移除后原「历史数据继承」第 0 步删除）：
    //   0 选择设备型号 → 1 选择节点&通道 → 2 基本信息
    if (createStep.value === 0) return !!selectedParser.value
    if (createStep.value === 1) {
      if (!deviceForm.node_id) return false
      if (channelTab.value === 'existing') return !!selectedChannel.value
      // D6(b)：该节点未上报所选总线资源时，不允许用硬编码资源名继续创建
      // （否则会造出指向不存在总线的通道）。hasBusOptions 为假时下拉本身已禁用，
      // 这里再把「下一步」一并挡住，避免用户以为能跳过。
      if (channelTab.value === 'create') return !!newChannel.hardware_id && hasBusOptions.value
    }
    if (createStep.value === 2) return !!deviceForm.name
    return false
  })

  /**
   * D6(c) + m-1（2026-09-22）：把「通道的总线资源标识」解析成可提交的
   * **EdgeDevice.hardware_id（设备地址）**。
   *
   * 语义边界（本次 UI 缺陷族的根因族）：
   *   · channel.hardware_id       = 总线资源标识（UART0 / SPI2 / I2C0）
   *   · edgeDevice.hardware_id    = 该设备在总线上的**地址**（Modbus 从站号 / I2C 地址，1-254）
   * 判定口径不在这里自造：复用 @/utils/deviceAddress（与后端 deviceaction.ParseHardwareAddress
   * 同一张表，由 DeviceAddressContractParity.spec.ts 双向钉死）。
   *
   * m-1：**是否要求地址由型号能力决定，而不是由字符串形态决定**。后端
   * validateEdgeDeviceAddress 只对 driverRequiresTargetAddress 为真的型号校验 1-254，
   * 纯 I2C/SPI 型号原样接受任何标识；改前这里一律按形态兜底成默认地址 1，于是给
   * 一个从不使用地址的型号凭空写了一个地址。三分支：
   *   · requires === true      → 合法地址原样提交；总线名 ⇒ 默认地址 1 **并显式提示**；
   *   · requires === false     → **不造地址**：提交空串（后端对空值的语义是「无地址/默认」），
   *                              也不弹无意义提示；
   *   · requires === undefined → 兼容分支（老后端不返回该字段、树未加载完、或型号只在
   *                              DB 模板里）：保持**改前行为**（默认地址 1 + 提示），
   *                              宁可与后端多校验一次，也不静默放行总线名。
   */
  function resolveInlineDeviceAddress(
    busHardwareId: string | undefined,
    requiresTargetAddress?: boolean,
  ): { value: string; warned: boolean } {
    const raw = String(busHardwareId ?? '').trim()
    const valid = isValidDeviceAddress(raw)
    const explicitValue = valid && parseDeviceAddress(raw).explicit ? raw : ''
    // 明确不消费地址的型号：不要凭空造一个它不用的地址（这是本任务的核心修复）。
    if (requiresTargetAddress === false) {
      return { value: '', warned: false }
    }
    if (valid) {
      return { value: explicitValue, warned: false }
    }
    // requires === true（真源要求地址）或 === undefined（能力未知，兼容旧后端）：
    // 退回后端默认地址 1，并由调用方显式提示。
    return { value: String(DEVICE_ADDRESS_DEFAULT), warned: true }
  }

  const onNewChannelBusTypeChange = () => {
    // 切换总线类型后，上一条资源 id 对新类型没有意义（UART0 不是 SPI 资源）。
    newChannel.hardware_id = ''
  }

  const selectParser = (parser: Parser) => {
    selectedParser.value = parser
    selectedChannel.value = null
    // EDGE-WIZ-004/005: 切换设备型号时清理旧型号的逐指令轮询间隔。
    // CreateWizardCommandIntervals 监听 deviceType 自动重置已加载指令;
    // 此处同步清空提交时使用的快照, 保证重选型号不带旧 intervals。
    commandIntervalsError.value = ''
    prepareCommandIntervals()
    // P0-1: 同步新通道硬件类型到解析器第一个可用总线,避免默认值 i2c 与
    // 解析器 hardware_types 不匹配(如选 uart 解析器但表单默认 i2c)。
    const buses = parser.hardware_types || []
    if (buses.length > 0 && !buses.includes(newChannel.hardware_type)) {
      newChannel.hardware_type = buses[0]
      newChannel.hardware_id = ''
    }
  }

  // ---- EDGE-WIZ-004/005: 创建向导逐指令轮询间隔 ----
  // 由 CreateWizardCommandIntervals 子组件 (仅创建模式) 加载驱动 schedulable
  // 指令并维护每条的间隔; 提交时经 freezeCommandIntervals 冻结到快照参与 payload。
  // commandIntervalsError: 驱动指令列表加载失败时的错误信息 (用于拦截静默提交)。
  const commandIntervalsRef = ref()
  const commandIntervalsReady = ref(false)
  const commandIntervalsSnapshot = ref<Record<string, number> | null>(null)
  const commandIntervalsError = ref('')

  // 当前型号驱动是否声明了 schedulable 轮询指令 — 有则隐藏全局 interval_ms
  // (避免误导), 无则保留原有单个采集间隔。
  const hasSchedulableCommands = computed(() => {
    const el = commandIntervalsRef.value as any
    if (!el || typeof el.schedulableCommands === 'undefined') return false
    return (el?.schedulableCommands || []).length > 0
  })

  const prepareCommandIntervals = () => {
    commandIntervalsReady.value = false
    commandIntervalsSnapshot.value = null
  }

  const handleDriverCommandsLoadError = (message: string) => {
    commandIntervalsError.value = message
    commandIntervalsReady.value = false
    commandIntervalsSnapshot.value = null
    feedback.error(message)
  }

  // 提交时一致快照。加载失败/进行中 → null (不携带任何 intervals, 更不会带
  // 旧驱动的数据); 就绪 → 全部 schedulable 指令的当前间隔。
  const freezeCommandIntervals = (): Record<string, number> | null => {
    const el = commandIntervalsRef.value as any
    if (!el || typeof el.getIntervals !== 'function') return commandIntervalsSnapshot.value
    const intervals = el.getIntervals()
    if (intervals === null) return commandIntervalsSnapshot.value
    commandIntervalsSnapshot.value = intervals
    commandIntervalsReady.value = true
    return intervals
  }

  const selectChannel = (ch: Channel) => {
    selectedChannel.value = ch
  }

  /**
   * 编辑模式的通道可选项：**当前节点**下、与设备型号总线类型相容的通道。
   *
   * 为什么按总线类型过滤：后端在保存时校验「通道的 hardware_type 与设备型号
   * 需要的总线相容」，把不相容的通道列出来等于给用户提供一个必然 400 的选项。
   * 型号未知（老数据 / 树未加载）时不过滤 —— 宁可多一个选项，也不要因为能力
   * 字段缺失就把唯一可用的通道藏起来。
   */
  const editChannels = computed<Channel[]>(() => {
    const nodeIdStr = String(deviceForm.node_id ?? '')
    if (!nodeIdStr) return []
    const parserBuses = (selectedParser.value?.hardware_types || []).map(b => b.toLowerCase())
    const deviceBus = String(deviceForm.hardware_type || '').toLowerCase()
    const accepted = parserBuses.length > 0 ? parserBuses : (deviceBus ? [deviceBus] : [])
    return channelStore.channels.filter((ch) => {
      if (!ch || String(ch.node_id) !== nodeIdStr) return false
      if (accepted.length === 0) return true
      return accepted.includes(String(ch.hardware_type || '').toLowerCase())
    })
  })
  const editChannelsLoading = computed(() => channelStore.loading)

  /**
   * 编辑模式切换「所属节点」：清空已选通道并拉取该节点的通道列表。
   *
   * 清空是**语义要求**而不是体验优化：通道 id 与节点绑定，旧通道一定不属于新节点，
   * 留着它提交必然被后端 400 拒绝（"channel does not belong to node"）。让用户
   * 重选一次，比让他看到一条看不懂的服务端错误要好。
   */
  const handleEditNodeChange = () => {
    deviceForm.channel_id = null
    const nodeId = deviceForm.node_id
    if (!nodeId) return
    // 通道 store 里可能只有上一个节点的通道；按新节点拉一次（后端按 node_id 过滤）。
    void channelStore.fetchChannels(nodeId).catch(() => {
      ElMessage.warning('该节点通道列表加载失败，请稍后重试或重新打开编辑框')
    })
  }

  const handleEdit = (device: any) => {
    // 加载设备详情并打开编辑对话框
    editingDeviceId.value = device.id
    // 填充表单数据
    deviceForm.name = device.name
    deviceForm.node_id = device.node_id
    // 通道绑定：打开时回填当前值；用户不动它 → 提交同一个 channel_id（后端校验通过）。
    deviceForm.channel_id = typeof device.channel_id === 'number' && device.channel_id > 0
      ? device.channel_id
      : null
    // 编辑模式没有「解析器选择」步骤（selectedParser 恒为 null）⇒ editChannels
    // 拿不到解析器总线列表，会退回用设备自身的 hardware_type 过滤通道。
    selectedParser.value = null
    // 拉该节点的通道（store 里可能只有别的节点的通道）
    if (device.node_id) {
      void channelStore.fetchChannels(device.node_id).catch(() => {
        // 失败时 editChannels 可能为空，下方会显示"该节点下没有可用通道"提示
      })
    }
    deviceForm.device_type = device.device_type
    deviceForm.protocol = device.protocol || 'modbus'
    deviceForm.hardware_type = device.hardware_type || 'uart'
    // 只接受**真实存储的设备地址**（normalize 已不再用通道总线名兜底）。
    // 注意这里不能用 device.channel_hardware_id 回填 —— 那是总线名，回填即回到 G4 的老路。
    deviceForm.hardware_id = device.hardware_id || ''
    deviceForm.interval_ms = device.config?.interval_ms || 1000
    // G4：快照打开时的地址值（保存时用它判断"用户是否真的改过地址"）。
    editingDeviceAddressSnapshot.value = deviceForm.hardware_id
    showCreateDialog.value = true
  }

  // 表单操作
  const handleCreate = async () => {
    if (!deviceFormRef.value) return
    const sessionGeneration = getSessionGeneration()
    const transactionGeneration = createTransactionGeneration

    try {
      await deviceFormRef.value.validate()
      assertSessionGeneration(sessionGeneration)
      if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      if (!deviceForm.node_id) {
        ElMessage.warning('请选择所属节点')
        return
      }
      submitting.value = true
      const frozenDeviceForm = { ...deviceForm }
      const frozenNewChannel = { ...newChannel }
      const frozenParser = selectedParser.value
        ? { ...selectedParser.value, hardware_types: [...(selectedParser.value.hardware_types || [])] }
        : null
      const matchingDeviceConfig = frozenParser
        ? availableTemplates.value.find(
            template => template.parser_id === frozenParser.id || template.device_type === frozenParser.id,
          )
        : undefined
      const deviceConfigId = frozenParser?.device_config_id ?? matchingDeviceConfig?.id
      const selectedChannelConfig = selectedChannel.value?.config
      const frozenSelectedChannel = selectedChannel.value
        ? {
            ...selectedChannel.value,
            config: typeof selectedChannelConfig === 'string'
              ? (() => { try { return JSON.parse(selectedChannelConfig) } catch { return {} } })()
              : { ...(selectedChannelConfig || {}) },
          }
        : null
      const frozenChannelTab = channelTab.value
      const frozenEditingDeviceId = editingDeviceId.value
      // D6(b) 提交侧兜底：步骤校验之外，创建路径再挡一次「未上报资源」。
      if (!frozenEditingDeviceId && frozenChannelTab === 'create' && !hasBusOptions.value) {
        ElMessage.warning(unavailableBusMessage.value)
        submitting.value = false
        return
      }

      // EDGE-WIZ-004/005: 提交流程先等驱动指令加载结算 (成功或失败), 再冻结
      // 逐指令轮询间隔快照。加载失败时以下拦截, 不允许携带旧驱动的 intervals
      // 静默提交。
      await (commandIntervalsRef.value as any)?.whenLoaded?.()
      const frozenCommandIntervals = freezeCommandIntervals()

      // 编辑模式 — 字段对齐后端 UpdateDTO: name/enabled/interval_ms/hardware_id/node_id/channel_id
      // 不传 type: 设备型号绑定在编辑中不可变,且后端 G1 在 device_config_id>0 时拒绝 type。
      if (frozenEditingDeviceId) {
        // G4 编辑-保存回路：编辑对话框里**没有**设备地址输入项（见步骤 3 的表单字段：
        // 只有名称 / 采集间隔）。改前这里无条件把 deviceForm.hardware_id 发出去，而该值
        // 来自 handleEdit 的 device.hardware_id —— normalize 的旧兜底会把它填成通道总线名
        // （"UART1"），于是"打开编辑框→点保存"就把总线名写回后端（设备地址字段）。
        // 现在有两道防线：
        //   ① normalize 不再跨语义兜底（api/edgeDevice.ts），所以这里是真实存储值；
        //   ② 这里只在"用户真的改了地址"时才带上该字段；值为空/与打开时相同则
        //      **完全不发**这个 key，后端就不会有任何 hardware_id 写入。
        const openedAddress = String(editingDeviceAddressSnapshot.value ?? '')
        const currentAddress = String(frozenDeviceForm.hardware_id ?? '')
        const addressChanged = currentAddress !== openedAddress
        // 契约 §4.1（R0 主路径）：node_id 与 channel_id **一起**提交。
        // 同一 edge_devices.id ⇒ unified_data.device_id 不变 ⇒ 换节点后历史连续。
        // 后端校验「新 channel 属于新 node」，只带 node_id 会 400，故前端在
        // canGoNext / 表单规则层已强制两者同时存在。
        if (!frozenDeviceForm.channel_id) {
          ElMessage.warning('请选择通道')
          submitting.value = false
          return
        }
        await edgeDeviceApi.update(frozenEditingDeviceId, {
          name: frozenDeviceForm.name,
          node_id: String(frozenDeviceForm.node_id!),
          channel_id: frozenDeviceForm.channel_id,
          interval_ms: frozenDeviceForm.interval_ms,
          // 只有地址真的变了才带上这个 key（见上方 ②）。展开空对象 = 该 key 完全不出现在
          // 请求体里，后端 UpdateDTO 的 *string 保持 nil ⇒ 不产生任何 hardware_id 写入。
          ...(addressChanged ? { hardware_id: currentAddress } : {}),
        })
        assertSessionGeneration(sessionGeneration)
        if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
        edgeDeviceStore.invalidateLists()
        edgeDeviceStore.invalidateDetail(frozenEditingDeviceId)
        ElMessage.success('设备更新成功')
        showCreateDialog.value = false
        resetCreateDialog()
        await fetchDevices(true)
        return
      }

      let channelId: number | undefined

      // 根据 parser 的 hardware_types 推断协议类型
      if (!frozenParser) throw new Error('请先选择设备型号')

      // EDGE-WIZ-004/005: 驱动指令加载失败必须显式处理 — 提示并让用户退回
      // 重选型号或点击重试, 绝不能静默提交 (旧驱动 intervals 已在 selectParser
      // 切换时清空, 此处兜底拦截)。
      if (commandIntervalsError.value) {
        ElMessage.warning('驱动轮询指令未成功加载，请重试或返回上一步重新选择设备型号')
        createStep.value = 3
        submitting.value = false
        return
      }

      // F: inline channel creation — no two-phase commit
      let targetChannel: Channel
      if (frozenChannelTab === 'create') {
        const address = frozenNewChannel.hardware_type === 'spi'
          ? frozenNewChannel.spi_cs.toString()
          : frozenNewChannel.address

        const channelPayload = {
          hardware_type: toChannelHardwareType(frozenNewChannel.hardware_type),
          hardware_id: frozenNewChannel.hardware_id,
          address: address || undefined,
          config: {
            interval_ms: frozenNewChannel.interval_ms,
            device_type: frozenParser.id,  // 传递 device_type 以便模板构建
          },
        }

        // F: 传 channel 子对象给后端,事务内创建通道+设备,消除孤儿通道风险
        const baseParams = deviceConfigId
          ? { device_config_id: deviceConfigId }
          : { type: frozenParser.id }

        // D6(c)：inline 创建路径里，EdgeDevice.hardware_id 是**设备地址**（Modbus 从站号 /
        // I2C 地址），而 channelPayload.hardware_id 是**总线资源标识**（UART0/SPI2）。
        // 二者语义不同，改前直接把总线标识当设备地址提交（前一个生产故障的根因族：
        // 派发时 ParseHardwareAddress 拒绝 "UART1"，操作静默卡 QUEUED 直到 deadline 过期）。
        // 这里：合法地址（含空/0 的默认语义）原样提交；非法（总线名形态）则退回后端默认地址 1
        // 并在确认卡片显式提示 —— 必须由用户到边缘设备详情页确认/修正实际从站地址。
        // m-1：能力来自**所选型号**（frozenParser 是提交时快照），不允许用 selectedParser
        // 之外的第二份状态，否则提交期间换型号会让地址判定与型号不一致。
        const inlineDeviceAddress = resolveInlineDeviceAddress(
          channelPayload.hardware_id,
          resolveModelRequiresTargetAddress(frozenParser.id),
        )
        if (inlineDeviceAddress.warned) {
          ElMessage.warning(`通道标识「${channelPayload.hardware_id}」是总线名称，不是设备地址；已按默认从站地址 ${DEVICE_ADDRESS_DEFAULT} 创建设备，请到设备详情确认或修正实际地址`)
        }
        await edgeDeviceApi.create({
          name: String(frozenDeviceForm.name),
          node_id: String(frozenDeviceForm.node_id!),
          hardware_id: inlineDeviceAddress.value,
          ...baseParams,
          // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
          ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
          channel: {
            hardware_type: channelPayload.hardware_type,
            hardware_id: channelPayload.hardware_id,
            address: address || undefined,
            config: channelPayload.config,
          },
        })
        assertSessionGeneration(sessionGeneration)
        if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
        // Refresh channel store to pick up the newly-created channel
        await channelStore.fetchChannels(frozenDeviceForm.node_id ?? undefined)
        // No downstream targetChannel/channelId are needed on the inline path —
        // the channel was created server-side inside the device-create tx and
        // is now visible via fetchChannels. Keep targetChannel as the form data
        // (no id) only to satisfy the existing-channel branch's type contract.
        targetChannel = {
          node_id: String(frozenDeviceForm.node_id ?? ''),
          hardware_type: toChannelHardwareType(frozenNewChannel.hardware_type),
          hardware_id: frozenNewChannel.hardware_id,
          address: frozenNewChannel.address || undefined,
          interval_ms: frozenNewChannel.interval_ms,
          config: {},
        }
      } else if (frozenSelectedChannel) {
        channelId = frozenSelectedChannel.id
        targetChannel = frozenSelectedChannel

        // 已有通道场景：如果 channel.config 没有 device_type，补上
        if (!channelConfigObject(targetChannel).device_type && targetChannel.id) {
          await channelStore.updateChannel(targetChannel.id, {
            config: JSON.stringify({
              ...channelConfigObject(targetChannel),
              device_type: frozenParser.id,
            }),
          })
          assertSessionGeneration(sessionGeneration)
          if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
        }

        // G3：已有通道分支与 inline-create 分支必须走**同一个**地址解析口径。
        // 改前这里直接复制 targetChannel.hardware_id（通道的总线资源标识 "UART1"），
        // 于是列表页向导的第二条创建路径绕过了 resolveInlineDeviceAddress，
        // 把总线名写进 EdgeDevice.hardware_id（设备从站地址字段）—— 与 2026-09-20
        // 生产故障同一根因，只是另一条端点/分支。
        // 现在：合法地址原样提交；总线名形态退回后端默认地址 1 并在提交前显式告知用户。
        // m-1：与 inline-create 分支同一个能力口径（同一型号 → 同一判定），也不允许
        // 把能力查询写在两个分支各自处，否则两条路径会再次漂移。
        const existingChannelDeviceAddress = resolveInlineDeviceAddress(
          targetChannel.hardware_id,
          resolveModelRequiresTargetAddress(frozenParser.id),
        )
        if (existingChannelDeviceAddress.warned) {
          ElMessage.warning(`通道标识「${targetChannel.hardware_id}」是总线名称，不是设备地址；已按默认从站地址 ${DEVICE_ADDRESS_DEFAULT} 创建设备，请到设备详情确认或修正实际地址`)
        }

        // 创建边缘设备: 根据 device_config_id 是否存在决定传 device_config_id 还是 type
        if (deviceConfigId) {
          await edgeDeviceApi.create({
            name: String(frozenDeviceForm.name),
            node_id: String(frozenDeviceForm.node_id!),
            channel_id: channelId!,
            hardware_id: existingChannelDeviceAddress.value,
            device_config_id: deviceConfigId,
              // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
            ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
          })
        } else {
          await edgeDeviceApi.create({
            name: String(frozenDeviceForm.name),
            node_id: String(frozenDeviceForm.node_id!),
            channel_id: channelId!,
            type: frozenParser.id,
            // G3：与上面 device_config_id 分支同一个解析结果（同一请求内只解析一次）
            hardware_id: existingChannelDeviceAddress.value,
              // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
            ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
          })
        }
      } else {
        throw new Error('请先选择或创建通道')
      }
      assertSessionGeneration(sessionGeneration)
      if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      edgeDeviceStore.invalidateLists()

      ElMessage.success('创建成功')
      showCreateDialog.value = false
      resetCreateDialog()
      await fetchDevices(true)
    } catch (error: any) {
      if (transactionGeneration === createTransactionGeneration) feedback.handleError(error, '创建失败')
    } finally {
      if (transactionGeneration === createTransactionGeneration) submitting.value = false
    }
  }

  const resetCreateDialog = () => {
    createStep.value = 0
    selectedParser.value = null
    selectedChannel.value = null
    channelTab.value = 'existing'
    // EDGE-WIZ-004/005: 清空逐指令间隔快照与加载错误
    commandIntervalsError.value = ''
    commandIntervalsSnapshot.value = null
    commandIntervalsReady.value = false
    deviceForm.name = ''
    deviceForm.node_id = null
    deviceForm.interval_ms = 1000
    newChannel.hardware_type = 'i2c'
    newChannel.hardware_id = ''
    newChannel.address = ''
    newChannel.spi_cs = 10
    newChannel.interval_ms = 1000
  }

  /** 归一化硬件类型到 Channel 契约（UI 表单是宽松 string，此处按白名单收窄，非法值回退 i2c）。 */
  const toChannelHardwareType = (value: string): Channel['hardware_type'] => {
    const lower = value.toLowerCase()
    return lower === 'uart' || lower === 'i2c' || lower === 'spi' || lower === 'adc'
      ? (lower as Channel['hardware_type'])
      : 'i2c'
  }

  /** 读通道 config 的对象形态（后端 Channel.Config 是 text 列，可能返回 JSON 字符串）。 */
  const channelConfigObject = (ch: Channel): { device_type?: string; [key: string]: unknown } => {
    if (typeof ch.config === 'string') {
      try {
        return JSON.parse(ch.config) ?? {}
      } catch {
        return {}
      }
    }
    return ch.config ?? {}
  }

  // 监听对话框打开，重置向导状态
  const handleCreateDialogClose = (done: () => void) => {
    if (submitting.value) return
    done()
  }

  // 设备上报的总线资源在进入"选择/创建通道"步骤时被消费，进入该步即确保已取到
  // （用户可能已在别处选过节点，或从后续步骤返回修改，handleNodeChange 不会再触发）。
  watch(createStep, (step) => {
    // 步骤编号 2026-10-23 整体前移一位（原「历史数据继承」第 0 步删除）：
    // 节点能力（buses 资源）在真正的「选择节点 & 通道」步（=1）加载。
    if (step === 1) void loadNodeCapabilities(deviceForm.node_id)
  })

  watch(showCreateDialog, (val) => {
    if (!val) {
      createTransactionGeneration++
      submitting.value = false
      createStep.value = 0
      selectedParser.value = null
      selectedChannel.value = null
      editingDeviceId.value = null
      // EDGE-WIZ-004/005: 关闭清空逐指令间隔快照与错误
      commandIntervalsError.value = ''
      commandIntervalsSnapshot.value = null
      commandIntervalsReady.value = false
      // 重置表单
      deviceForm.name = ''
      deviceForm.node_id = null
      deviceForm.device_type = ''
      deviceForm.protocol = 'modbus'
      deviceForm.hardware_type = 'uart'
      deviceForm.hardware_id = ''
      deviceForm.interval_ms = 1000
    } else {
      void loadCreateWizardData()
    }
  })

  // ── 子组件模板 ref 的显式回写 ────────────────────────────────────────
  // 对话框模板已搬进 EdgeDeviceCreateDialog.vue：字符串模板 ref 只会写进**子组件**的
  // setupState，拿不到这里的变量。改用 function-ref 显式回写，ref 变量仍在本作用域，
  // handleCreate / freezeCommandIntervals 的读取口径一字未变（只换了「谁把实例写进来」）。
  const setDeviceFormRef = (el: unknown) => { deviceFormRef.value = el }
  const setChannelFormRef = (el: unknown) => { channelFormRef.value = el }
  const setCommandIntervalsRef = (el: unknown) => { commandIntervalsRef.value = el }

  // 组件卸载：自增事务代号，让在飞事务在下一个 await 后失效（原页面 onUnmounted 的同义搬移）。
  onUnmounted(() => {
    createTransactionGeneration++
  })

  return {
    // 对话框开关（页面持有的同一 ref；子组件 v-model 直接读写它）
    showCreateDialog,
    // 三步向导状态与表单
    createStep, selectedParser, selectedChannel, channelTab,
    availableTemplates, selectedTemplateId, selectedTemplate,
    loadTemplates, onTemplateSelected,
    editingDeviceId, editingDeviceAddressSnapshot,
    deviceForm, deviceRules, newChannel,
    channelFormRef, deviceFormRef,
    submitting,
    // 节点 / 型号 / 总线资源能力（「硬件ID」下拉的唯一真源）
    collectors, fetchNodes, onlineCollectors,
    driverTree, driverTreeLoaded, resolveModelRequiresTargetAddress, fetchDriverTreeOnce,
    nodeCapabilitiesByNode, capabilitiesLoading, loadNodeCapabilities,
    busResourcesForType, busTypeReported, hasBusOptions, unavailableBusMessage, availableBusesForType,
    availableParsers, selectedParserBusTypes, existingChannels, generatedChannelName,
    canGoNext, resolveInlineDeviceAddress, onNewChannelBusTypeChange, selectParser, selectChannel,
    // EDGE-WIZ-004/005 逐指令轮询间隔
    commandIntervalsRef, commandIntervalsReady, commandIntervalsSnapshot, commandIntervalsError,
    hasSchedulableCommands, prepareCommandIntervals, handleDriverCommandsLoadError, freezeCommandIntervals,
    // 编辑模式（换节点 / 换通道）
    editChannels, editChannelsLoading, handleEditNodeChange, handleEdit,
    // 提交事务与重置
    handleCreate, resetCreateDialog, handleCreateDialogClose,
    toChannelHardwareType, channelConfigObject,
    // 向导数据加载 / 节点切换
    loadCreateWizardData, handleNodeChange,
    // 子组件模板 ref 的显式回写
    setDeviceFormRef, setChannelFormRef, setCommandIntervalsRef,
  }
}
