/**
 * 创建向导的**数据加载编排**：节点列表 / 配置模板 / 通道列表刷新 / 切换节点。
 *
 * 边界：这一块只负责「什么时候去取什么」，不碰提交（提交在 useEdgeDeviceCreate.handleCreate）
 * 也不碰可选项派生（在 useCreateWizardSelection）。
 *
 * EDGE-WIZ-003：打开向导时**必须强制刷新**通道列表 —— 节点页刚创建的通道若命中旧缓存，
 * 用户会看不到它；通道 store 自身有 requestSequence/cacheEpoch 防竞态。
 */
import { computed, ref, type Ref } from 'vue'
import { useChannelStore } from '@/stores/channel'
import { useNodeStore } from '@/stores/node'
import { useParserStore } from '@/stores/parser'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import { feedback } from '@/utils/feedback'
import type { Channel } from '@/api/channel'

export interface CreateWizardDataDeps {
  availableTemplates: Ref<DeviceConfig[]>
  selectedChannel: Ref<Channel | null>
  deviceForm: { node_id: number | null }
  loadNodeCapabilities: (nodeId: string | number | null | undefined) => Promise<void>
  fetchDriverTreeOnce: () => Promise<void>
}

export function useCreateWizardData(deps: CreateWizardDataDeps) {
  const { availableTemplates, selectedChannel, deviceForm, loadNodeCapabilities, fetchDriverTreeOnce } = deps
  const nodeStore = useNodeStore()
  const channelStore = useChannelStore()
  const parserStore = useParserStore()

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

  // 节点列表：只被创建向导的「所属节点」下拉消费（原 useEdgeDeviceCreate 的 state 块）。
  const collectors = ref<any[]>([])

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

  return { collectors, onlineCollectors, fetchNodes, loadCreateWizardData, handleNodeChange }
}
