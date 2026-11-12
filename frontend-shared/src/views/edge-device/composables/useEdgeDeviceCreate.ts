/**
 * 边缘设备「创建 / 编辑」对话框的状态与提交事务（2026-11-12 第三轮拆分：再切子域）。
 *
 * 本文件现在只保留四件事：
 *   ① **三步向导状态**（createStep / selectedParser / selectedChannel / channelTab /
 *      availableTemplates / deviceForm / deviceRules / newChannel / 三个模板 ref / 事务代号）；
 *   ② **提交事务 handleCreate**（inline 建通道 / 已有通道 / 编辑 update 三条分支，
 *      G4「改了才发 hardware_id」、EDGE-WIZ-004 逐指令间隔、会话代际与事务作废全部原样）；
 *   ③ **编排**：把上面 ① 的 ref 传给 5 个子域组合式，并统一 return 给页面/对话框；
 *   ④ watch / onUnmounted / function-ref setter。
 *
 * 子域（全部纯搬移，各自只吃必要的 ref，不 new 第二份状态）：
 *   · edgeDeviceAddress.ts                —— 型号地址能力 + 地址解析（零 Vue 依赖的纯函数）
 *   · composables/useDeviceDriverTree.ts  —— 驱动树快照（页面与这里共用同一份）
 *   · composables/useDeviceBusResources.ts —— 节点能力上报 → 总线资源可选项
 *   · composables/useDeviceCommandIntervals.ts —— EDGE-WIZ-004/005 逐指令轮询间隔
 *   · composables/useCreateWizardSelection.ts —— 型号/通道选择与步骤校验（纯派生）
 *   · composables/useEdgeDeviceEdit.ts    —— 编辑模式（换节点 / 换通道）
 *   · composables/useCreateWizardData.ts  —— 节点/模板/通道列表的数据加载编排
 *
 * 为什么 handleCreate 不拆：它的三条分支共享同一份 frozen 快照（表单/通道/型号）、
 * 同一条会话代际与事务代号检查，拆开会让「改坏哪里会变红」的判据跨模块漂移；
 * 因此提交事务整体留在本文件，地址判定与逐指令间隔只搬走了**纯读取/纯判定**的部分。
 */
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useChannelStore } from '@/stores/channel'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { edgeDeviceApi } from '@/api/edgeDevice'
import type { DeviceConfig } from '@/api/deviceConfig'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'
import { feedback } from '@/utils/feedback'
import { DEVICE_ADDRESS_DEFAULT } from '@/utils/deviceAddress'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { resolveInlineDeviceAddress } from '../edgeDeviceAddress'
import { useDeviceBusResources } from './useDeviceBusResources'
import { useDeviceCommandIntervals } from './useDeviceCommandIntervals'
import { useCreateWizardSelection } from './useCreateWizardSelection'
import { useEdgeDeviceEdit } from './useEdgeDeviceEdit'
import { useCreateWizardData } from './useCreateWizardData'

/** 创建 / 编辑向导需要的页面侧依赖：组合式不持有它们，避免第二份状态。 */
export interface EdgeDeviceCreateDeps {
  /** 对话框开关由页面持有（工具栏「创建边缘设备」直接置 true），组合式只读写同一个 ref。 */
  showCreateDialog: Ref<boolean>
  /**
   * 列表刷新。页面里的 fetchDevices 在本组合式**之后**声明，故只能传惰性适配器：
   * setup 期不会调用它，只有 handleCreate 在 await 之后才用（与 useEdgeDeviceBatch 同一约定）。
   */
  fetchDevices: (force?: boolean, throwOnError?: boolean) => Promise<void>
  /** 驱动树快照的取数（页面持有同一份，见 useDeviceDriverTree.ts）。 */
  fetchDriverTreeOnce: () => Promise<void>
  /** 型号是否消费设备地址（同一份快照上的纯判定，见 edgeDeviceAddress.ts）。 */
  resolveModelRequiresTargetAddress: (deviceType: string | undefined) => boolean | undefined
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
  const {
    showCreateDialog, fetchDevices,
    fetchDriverTreeOnce, resolveModelRequiresTargetAddress,
  } = deps
  const channelStore = useChannelStore()
  const edgeDeviceStore = useEdgeDeviceStore()

  // 提交中标志 / 「创建事务代号」：关闭对话框或组件卸载都会自增代号，
  // 让仍在飞行中的 create / update 在下一个 await 之后作废（见 handleCreate 的两道检查）。
  const submitting = ref(false)
  let createTransactionGeneration = 0

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

  // ── 子域组合式（2026-11-12 第三轮拆分，均为纯搬移）──────────────────────
  // 状态仍由本文件创建，子域只吃「自己需要的那几个 ref/函数」；
  // 因此页面解构、对话框 ctl、既有门禁用例看到的仍是同一批对象（不是副本）。
  const {
    nodeCapabilitiesByNode, capabilitiesLoading, loadNodeCapabilities,
    busResourcesForType, busTypeReported, hasBusOptions, unavailableBusMessage, availableBusesForType,
  } = useDeviceBusResources({ deviceForm, newChannel })

  const {
    commandIntervalsRef, commandIntervalsReady, commandIntervalsSnapshot, commandIntervalsError,
    hasSchedulableCommands, prepareCommandIntervals, handleDriverCommandsLoadError, freezeCommandIntervals,
    setCommandIntervalsRef,
  } = useDeviceCommandIntervals()

  // 型号 / 通道选择与步骤校验：依赖上面两块的「是否有可选资源 / 指令是否加载成功」结论
  const {
    availableParsers, selectedParserBusTypes, existingChannels, generatedChannelName, canGoNext,
    onNewChannelBusTypeChange, selectParser, selectChannel,
  } = useCreateWizardSelection({
    deviceForm, newChannel, selectedParser, selectedChannel, createStep, channelTab,
    hasBusOptions, commandIntervalsError, prepareCommandIntervals,
  })

  const { editChannels, editChannelsLoading, handleEditNodeChange, handleEdit } = useEdgeDeviceEdit({
    deviceForm, selectedParser, editingDeviceId, editingDeviceAddressSnapshot, showCreateDialog,
  })

  const { collectors, onlineCollectors, fetchNodes, loadCreateWizardData, handleNodeChange } = useCreateWizardData({
    availableTemplates, selectedChannel, deviceForm, loadNodeCapabilities, fetchDriverTreeOnce,
  })

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
  // handleCreate 的读取口径一字未变（只换了「谁把实例写进来」）。
  // （commandIntervalsRef 的回写随逐指令间隔一起搬到了 useDeviceCommandIntervals.ts。）
  const setDeviceFormRef = (el: unknown) => { deviceFormRef.value = el }
  const setChannelFormRef = (el: unknown) => { channelFormRef.value = el }

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
    onTemplateSelected,
    editingDeviceId, editingDeviceAddressSnapshot,
    deviceForm, deviceRules, newChannel,
    channelFormRef, deviceFormRef,
    submitting,
    // 节点 / 型号 / 总线资源能力（「硬件ID」下拉的唯一真源）
    collectors, fetchNodes, onlineCollectors,
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
