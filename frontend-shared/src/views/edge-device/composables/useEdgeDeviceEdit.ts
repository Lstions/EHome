/**
 * 编辑模式（契约 §4.1：换节点 / 换通道后历史数据仍连续）。
 *
 * 边界：只做「编辑框打开时的回填」与「编辑态通道可选项」；提交事务仍在
 * useEdgeDeviceCreate.handleCreate 里（编辑分支共用同一条会话代际/事务作废逻辑，
 * 拆开会让「打开编辑框 → 保存」的 G4 判据跨模块漂移）。
 *
 * 状态由创建组合式传入（同一批 ref），本模块不 new 任何状态。
 */
import { computed, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useChannelStore } from '@/stores/channel'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'

export interface EdgeDeviceEditDeps {
  deviceForm: {
    node_id: number | null
    channel_id: number | null
    name: string
    device_type: string
    protocol: string
    hardware_type: string
    hardware_id: string
    interval_ms: number
  }
  selectedParser: Ref<Parser | null>
  editingDeviceId: Ref<number | null>
  editingDeviceAddressSnapshot: Ref<string>
  showCreateDialog: Ref<boolean>
}

export function useEdgeDeviceEdit(deps: EdgeDeviceEditDeps) {
  const { deviceForm, selectedParser, editingDeviceId, editingDeviceAddressSnapshot, showCreateDialog } = deps
  const channelStore = useChannelStore()

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

  return { editChannels, editChannelsLoading, handleEditNodeChange, handleEdit }
}
