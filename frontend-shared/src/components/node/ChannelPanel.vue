<template>
  <div class="bus-config-panel">
    <el-tabs v-model="activeTab">
      <el-tab-pane label="通道列表" name="channels">
        <div class="config-section">
          <div class="section-header">
            <h3>硬件资源与通道关联</h3>
            <div class="header-actions">
              <el-button size="small" @click="refreshChannels" :loading="channelsLoading">
                <el-icon><Refresh /></el-icon>
                刷新通道
              </el-button>
              <el-button size="small" @click="refreshBuses" :loading="loading">
                <el-icon><Refresh /></el-icon>
                同步硬件
              </el-button>
              <el-button size="small" type="primary" @click="saveBusConfig" :loading="saving" :disabled="collectorStatus !== 'online'">
                保存配置
              </el-button>
              <el-button type="primary" size="small" @click="handleOpenChannelManager()" :disabled="collectorStatus !== 'online'">
                <el-icon><Plus /></el-icon>
                新建通道
              </el-button>
              <el-button type="warning" size="small" @click="reconfigureDialogVisible = true" :disabled="collectorStatus !== 'online' || uartChannels.length === 0">
                改波特率
              </el-button>
            </div>
          </div>

          <!-- 加载中骨架屏 -->
          <template v-if="contentLoading">
            <el-skeleton :rows="8" animated />
          </template>

          <!-- 加载完成后的内容 -->
          <template v-else>

          <!-- 按硬件类型分组展示 -->
          <div v-for="busType in ['i2c', 'uart', 'spi', 'gpio', 'adc']" :key="busType" class="channel-bus-group">
            <template v-if="getChannelsByBusType(busType).length > 0 || hardware[busType]?.length > 0">
              <el-collapse>
                <el-collapse-item>
                  <template #title>
                    <div class="bus-type-header">
                      <el-tag :type="getBusTagType(busType)" size="small">{{ busType.toUpperCase() }}</el-tag>
                      <span class="bus-type-name">{{ getBusTypeName(busType) }}</span>
                      <span class="bus-count-badge">{{ hardware[busType]?.length || 0 }} 个资源</span>
                    </div>
                  </template>

                  <!-- 该总线类型下的所有硬件资源 -->
                  <div class="hardware-channel-list">
                    <div
                      v-for="hw in hardware[busType]"
                      :key="hw.id"
                      class="hardware-channel-item"
                      :class="{ 'has-channel': getChannelsForHardware(busType, hw.id).length > 0 }"
                    >
                      <!-- 第一行：硬件名称 + 类型标签 + 参数信息 + DMA开关 -->
                      <div class="hardware-card-header">
                        <div class="hardware-main">
                          <span class="hardware-id">{{ hw.name || hw.id }}</span>
                          <el-tag :type="getBusTagType(busType)" size="small" effect="plain">{{ busType.toUpperCase() }}</el-tag>
                          <PinBadges :pins="hw.pins" />
                          <span class="hardware-info">{{ getHardwareInfo(hw) }}</span>
                        </div>
                        <div class="hardware-actions" @click.stop>
                          <!-- DMA 开关（仅对有 DMA 支持的总线类型显示） -->
                          <!-- model-value: 该DMA是否绑定到当前硬件 -->
                          <!-- disabled: 该DMA已分配给其他硬件，或节点离线 -->
                          <template v-if="supportsDma(busType)">
                            <el-switch
                              v-for="dma in getDmaForHardware(busType, hw)"
                              :key="dma.dma_id"
                              :model-value="getDmaSwitchModel(busType, hw, dma)"
                              :disabled="!canToggleDma(dma, busType, hw)"
                              :loading="dmaToggling[dma.dma_id] || false"
                              @change="(val: boolean) => toggleDmaForHardware(busType, hw, dma, val)"
                              size="small"
                              :active-text="dma.name"
                              class="dma-switch"
                            />
                          </template>
                          <el-checkbox v-model="hw.enabled" disabled size="small" class="hw-enabled-checkbox" />
                        </div>
                      </div>

                      <!-- 第二行：通道标签 -->
                      <div class="hardware-card-channels">
                        <template v-if="getChannelsForHardware(busType, hw.id).length > 0">
                          <el-tag
                            v-for="ch in getChannelsForHardware(busType, hw.id)"
                            :key="ch.id"
                            size="default"
                            closable
                            type="primary"
                            effect="light"
                            class="channel-tag"
                            @close="collectorStatus === 'online' && handleDeleteChannel(ch.id)"
                            @click="collectorStatus === 'online' && handleOpenChannelManager(ch, busType, hw.id)"
                          >
                            <span class="channel-tag-name">{{ ch.name || '未命名' }}</span>
                            <span class="channel-tag-id">#{{ ch.id }}</span>
                          </el-tag>
                        </template>
                        <template v-else>
                          <div class="no-channel-hint">
                            <span class="no-channel-dashed">
                              <span class="no-channel-text">无通道</span>
                              <el-button
                                type="primary"
                                link
                                size="small"
                                class="add-channel-btn"
                                @click="collectorStatus === 'online' && handleOpenChannelManager(undefined, busType, hw.id)"
                              >
                                + 创建通道
                              </el-button>
                            </span>
                          </div>
                        </template>
                      </div>
                    </div>
                  </div>
                </el-collapse-item>
              </el-collapse>
            </template>
          </div>

          <el-empty v-if="allChannels.length === 0 && !channelsLoading" description="暂无通道" />
          </template>
        </div>
      </el-tab-pane>

      <el-tab-pane label="通道终端" name="terminal">
        <div class="config-section">
          <ChannelTerminal
            :collector-id="collectorId"
            :node-device-id="nodeDeviceId"
            :channels="allChannels"
          />
        </div>
      </el-tab-pane>
    </el-tabs>

    <!-- 通道管理对话框 -->
    <ChannelManager
      v-model="channelManagerVisible"
      :collector-id="collectorId"
      :collector-status="collectorStatus"
      :readonly="collectorStatus !== 'online'"
      :capabilities="capabilities"
      :initial-data="editingChannelData"
      :preset-hardware-type="presetHardwareType"
      :preset-hardware-id="presetHardwareId"
      @refresh="refreshChannels"
    />

    <!-- 改波特率对话框 -->
    <el-dialog v-model="reconfigureDialogVisible" title="修改波特率" width="400px" destroy-on-close>
      <el-form :model="reconfigureForm" label-width="80px">
        <el-form-item label="选择通道">
          <el-select v-model="reconfigureForm.channelId" placeholder="请选择 UART 通道" style="width: 100%">
            <el-option
              v-for="ch in uartChannels"
              :key="ch.id"
              :label="`${ch.name || '未命名'} (${ch.hardware_id})`"
              :value="ch.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="新波特率">
          <el-select v-model="reconfigureForm.baudrate" placeholder="请选择波特率" style="width: 100%">
            <el-option :value="9600" label="9600" />
            <el-option :value="19200" label="19200" />
            <el-option :value="38400" label="38400" />
            <el-option :value="57600" label="57600" />
            <el-option :value="115200" label="115200" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="reconfigureDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="reconfigureLoading" @click="submitReconfigure">确认</el-button>
      </template>
    </el-dialog>

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, computed, defineComponent, h } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, Loading, Plus } from '@element-plus/icons-vue'
import { nodeApi } from '@/api/node'
import { deviceConfigApi } from '@/api/deviceConfig'
import { channelApi } from '@/api/channel'
import ChannelManager from '@/components/channel/ChannelManager.vue'
import ChannelTerminal from '@/components/channel/ChannelTerminal.vue'
import { logger } from '@/utils/logger'
import type { DmaChannelInfo } from '@/api/node'

interface Props {
  collectorId: number | string
  nodeDeviceId?: string   // Device UUID like "404CCAFFFE57"
  collectorStatus?: string
  dmaChannels?: DmaChannelInfo[]
}

const props = defineProps<Props>()

const activeTab = ref('channels')
const initialLoadingDone = ref(false)
const loading = ref(false)
const channelsLoading = ref(false)
const busesLoaded = ref(false)
const channelsLoaded = ref(false)

// 统一 loading 状态：初始加载未完成 或 手动刷新中
const contentLoading = computed(() => !initialLoadingDone.value || loading.value || channelsLoading.value)
const saving = ref(false)
const channelManagerVisible = ref(false)
const editingChannelData = ref<any>(null)
const presetHardwareType = ref<string>('')
const presetHardwareId = ref<string>('')

// 改波特率
const reconfigureDialogVisible = ref(false)
const reconfigureLoading = ref(false)
const reconfigureForm = reactive({
  channelId: null as number | null,
  baudrate: 115200
})
const uartChannels = computed(() => allChannels.value.filter((ch: any) => ch.hardware_type === 'uart'))

// 配置模板列表
const configTemplates = ref<any[]>([])

// 采集器能力
const capabilities = ref<any>(null)

// 默认资源列表（仅用于空状态保底）
const emptyBuses = {
  gpio: [] as any[],
  adc: [] as any[],
  i2c: [] as any[],
  spi: [] as any[],
  uart: [] as any[]
}

const hardware = ref<any>(JSON.parse(JSON.stringify(emptyBuses)))

const allChannels = ref<any[]>([])

const getBusTagType = (type: string) => {
  const types: Record<string, string> = {
    gpio: 'info',
    adc: 'success',
    i2c: 'warning',
    spi: 'danger',
    uart: 'primary',
    pwm: 'info'
  }
  return types[type] || 'info'
}

const formatTime = (timestamp: number) => {
  return new Date(timestamp).toLocaleString('zh-CN')
}

// 获取可关联的配置模板（过滤同类型且未被占用的）
const getAvailableConfigs = (busType: string, currentBusId: string) => {
  // 获取同类型的所有模板
  const sameTypeConfigs = configTemplates.value.filter(cfg => cfg.hardware_type === busType)
  
  // 获取已经被其他总线关联的模板ID
  const usedConfigIds = new Set<number>()
  for (const type of ['gpio', 'adc', 'i2c', 'spi', 'uart']) {
    for (const bus of hardware.value[type] || []) {
      if (bus.config_id && bus.id !== currentBusId) {
        usedConfigIds.add(bus.config_id)
      }
    }
  }
  
  // 返回未被占用的模板
  return sameTypeConfigs.filter(cfg => !usedConfigIds.has(cfg.id))
}

// 根据ID获取配置模板
const getConfigById = (configId: number) => {
  return configTemplates.value.find(cfg => cfg.id === configId)
}

// 获取采集器角色（与传感器角色互补）
const getCollectorMode = (bus: any) => {
  if (bus.config_id) {
    const config = getConfigById(bus.config_id)
    if (config?.config?.sensor_role) {
      // 传感器是从机 → 采集器是主机
      // 传感器是主机 → 采集器是从机
      return config.config.sensor_role === 'slave' ? 'master' : 'slave'
    }
  }
  // 没有关联模板时，使用总线自身的mode字段
  return bus.mode || 'master'
}

// 关联配置模板时的处理
const onConfigAssociate = (busType: string, index: number) => {
  const bus = hardware.value[busType][index]
  if (bus.config_id) {
    const config = getConfigById(bus.config_id)
    if (config?.config) {
      // 从模板同步参数到总线配置
      if (config.config.clock_hz) {
        bus.freq_hz = config.config.clock_hz
        bus.clock_hz = config.config.clock_hz
      }
      // 标记为启用
      bus.enabled = true
    }
  }
}

// 加载配置模板列表
const loadConfigTemplates = async () => {
  try {
    const result = await deviceConfigApi.getList({ page_size: 100 })
    configTemplates.value = result.items || result.list || []
  } catch (error: any) {
    logger.error('加载配置模板失败', { error: String(error) })
  }
}

// 检查初始加载是否全部完成
const checkInitialDone = () => {
  if (busesLoaded.value && channelsLoaded.value) {
    initialLoadingDone.value = true
  }
}

const refreshBuses = async () => {
  loading.value = true
  try {
    // Step 0: 向采集器下发 QueryResources，等待 ReportResources 回填 DB
    try {
      await nodeApi.queryResources(props.collectorId)
      // 等采集器上报 ReportResources（通常 1-2 秒内）
      await new Promise(resolve => setTimeout(resolve, 2000))
    } catch (err) {
      logger.warn('QueryResources 下发失败（或采集器离线），直接从 DB 读取', { error: String(err) })
    }

    // Step 1: 获取设备能力（纯 Hardware，不含用户配置）
    const capData = await nodeApi.getCapabilities(props.collectorId)
    capabilities.value = capData  // 保存到 ref，供 ChannelManager 使用

    // Step 2: 获取用户配置（HardwareConfig）
    const userConfig = await nodeApi.getHardwareConfig(props.collectorId)

    // Step 3: 先在临时变量中构建合并结果，避免中间状态导致 UI 闪烁
    const mergedHardware: Record<string, any[]> = {}
    const capHardware = capData?.buses || {}
    for (const type of ['gpio', 'adc', 'i2c', 'spi', 'uart']) {
      if (capHardware[type] && Array.isArray(capHardware[type])) {
        mergedHardware[type] = capHardware[type].map((r: any) => {
          // 从实际 DMA 状态初始化 _dmaEnabled/_dmaId（而非硬编码 false）
          let dmaEnabled = false
          let dmaId: number | null = null
          if (props.dmaChannels) {
            for (const dma of props.dmaChannels) {
              if (dma.bound_to && dma.state === 1) {  // state=1 = allocated
                // bound_to format: "UART_CH39" or "busType/hwId"
                const boundStr = typeof dma.bound_to === 'string' ? dma.bound_to : ''
                const hwId = String(r.id)
                // 1) "UART_CH39" pattern: extract hw_type from bound_to ("UART") and match by channel
                if (boundStr.includes('_CH')) {
                  // boundStr = "UART_CH39" -> find channel 39, check its hardware_id
                  const chIdMatch = boundStr.match(/_CH(\d+)$/)
                  if (chIdMatch) {
                    const chId = parseInt(chIdMatch[1])
                    const ch = allChannels.value.find(c => c.id === chId)
                    if (ch && (ch.hardware_id === hwId || String(ch.hardware_id) === hwId)) {
                      dmaEnabled = true; dmaId = dma.dma_id; break
                    }
                  }
                }
                // 2) "busType/hwId" pattern: e.g. "uart/UART1"
                if (boundStr.includes('/' + hwId) || boundStr.endsWith(hwId)) {
                  dmaEnabled = true; dmaId = dma.dma_id; break
                }
              }
            }
          }
          return {
            ...r,
            enabled: false,  // 默认禁用
            config_id: null,
            _dmaEnabled: dmaEnabled,
            _dmaId: dmaId
          }
        })
      } else {
        mergedHardware[type] = []
      }
    }

    // Step 4: 合并用户配置（覆盖 enabled 和 config_id）到临时变量
    const userBuses = userConfig?.hardware?.buses || {}
    for (const [type, resources] of Object.entries(userBuses)) {
      if (Array.isArray(resources)) {
        for (const userResource of resources as any[]) {
          const local = mergedHardware[type]?.find((r: any) => r.id === userResource.id)
          if (local) {
            local.enabled = userResource.enabled ?? local.enabled
            local.config_id = userResource.config_id ?? local.config_id
            // 同步其他配置参数
            for (const key of Object.keys(userResource)) {
              if (key !== 'id' && key !== 'enabled' && key !== 'config_id') {
                local[key] = userResource[key]
              }
            }
          }
        }
      }
    }

    // Step 5: 一次性赋值，避免中间空状态闪烁
    hardware.value = mergedHardware

    // 初始加载时不弹成功提示，只有用户手动点击才弹
    if (initialLoadingDone.value) {
      ElMessage.success('配置已刷新')
    }
    // refreshBuses 完成后清除所有 DMA override，让 UI 回归真实状态
    dmaOverrideMap.value = {}
  } catch (error: any) {
    logger.error('获取配置失败', { error: String(error) })
    if (initialLoadingDone.value) {
      ElMessage.warning('获取配置失败，使用空配置')
    }
  } finally {
    loading.value = false
    busesLoaded.value = true
    checkInitialDone()
  }
}

const saveBusConfig = async () => {
  saving.value = true
  try {
    await nodeApi.updateHardwareConfig(props.collectorId, hardware.value)
    ElMessage.success('总线配置已保存')
  } catch (error: any) {
    ElMessage.error('保存失败: ' + (error.message || '未知错误'))
  } finally {
    saving.value = false
  }
}

const refreshChannels = async () => {
  channelsLoading.value = true
  try {
    const result = await channelApi.getList(props.nodeDeviceId || props.collectorId)
    // Handle both array and {items: []} response
    allChannels.value = Array.isArray(result) ? result : (result.items || [])
    if (initialLoadingDone.value) {
      ElMessage.success('通道列表已刷新')
    }
  } catch (error: any) {
    logger.error('获取通道列表失败', { error: String(error) })
    allChannels.value = []
  } finally {
    channelsLoading.value = false
    channelsLoaded.value = true
    checkInitialDone()
  }
}

// 获取指定硬件类型的所有通道
const getChannelsByBusType = (busType: string) => {
  return allChannels.value.filter(ch => ch.hardware_type === busType)
}

// 获取指定硬件资源下的所有通道
// hardwareId: 来自 hw.id，如 "1"（I2C）, "30"（GPIO）, "40"（ADC）等
const getChannelsForHardware = (busType: string, hardwareId: string) => {
  return allChannels.value.filter(ch => {
    const chType = ch.hardware_type?.toLowerCase()
    if (chType !== busType) return false

    const hwIdStr = String(hardwareId || '').toUpperCase()
    const chId = String(ch.hardware_id || '').toUpperCase()

    // 精确匹配 id 字符串，或匹配数字ID（I2C id=1, SPI id=10+, UART id=20+, GPIO id=30+, ADC id=40+）
    if (chId === hwIdStr) return true

    // 数字ID映射到名称
    const idNum = parseInt(chId)
    const hwIdMatch = nameToHardwareId(hwIdStr)
    if (!isNaN(idNum) && idNum === hwIdMatch) return true

    return false
  })
}

// 根据 hardware name 推算其 numeric id
// I2C: id=1,2,...; SPI: id=10,11,...; UART: id=20,21,...; GPIO: id=30,31,...; ADC: id=40,41,...
const nameToHardwareId = (name: string): number => {
  const upper = name.toUpperCase()
  if (upper.startsWith('I2C')) return parseInt(name.replace(/\D/g, '')) || 1
  if (upper.startsWith('SPI')) return 10 + (parseInt(name.replace(/\D/g, '')) || 0)
  if (upper.startsWith('UART')) return 20 + (parseInt(name.replace(/\D/g, '')) || 0)
  if (upper.startsWith('GPIO')) return 30 + (parseInt(name.replace(/\D/g, '')) || 0)
  if (upper.startsWith('ADC')) return 40 + (parseInt(name.replace(/\D/g, '')) || 0)
  return 0
}

// 获取硬件类型的显示名称
const getBusTypeName = (busType: string) => {
  const names: Record<string, string> = {
    gpio: '通用输入输出',
    adc: '模数转换',
    i2c: 'I2C 总线',
    spi: 'SPI 总线',
    uart: '串口通信'
  }
  return names[busType] || busType
}

// 获取硬件资源详细信息
// PinRole 枚举到中文标签的映射（proto 枚举值是数字）
const PIN_ROLE_LABELS: Record<number, string> = {
  0: '',           // PIN_ROLE_UNSPECIFIED
  1: 'TX',        // PIN_ROLE_TX
  2: 'RX',        // PIN_ROLE_RX
  3: 'SDA',       // PIN_ROLE_SDA
  4: 'SCL',       // PIN_ROLE_SCL
  5: 'MOSI',      // PIN_ROLE_MOSI
  6: 'MISO',      // PIN_ROLE_MISO
  7: 'SCLK',      // PIN_ROLE_SCLK
  8: 'CS',        // PIN_ROLE_CS
  9: 'GPIO',      // PIN_ROLE_GPIO
  10: 'CH',       // PIN_ROLE_CHANNEL
}

// PinBadges: 显示引脚信息的内联组件
const PinBadges = defineComponent({
  name: 'PinBadges',
  props: {
    pins: { type: Array, default: () => [] }
  },
  setup(props) {
    // 按总线类型分配配色主题
    const getRoleTheme = (role: number): { bg: string; fg: string; chipBg: string; chipFg: string } => {
      // SDA/SCL → I2C 青蓝系
      if (role === 3 || role === 4) return { bg: '#e6f7ff', fg: '#0077cc', chipBg: '#cce8ff', chipFg: '#0055aa' }
      // MOSI/MISO/SCLK/CS → SPI 紫粉系
      if (role >= 5 && role <= 8) return { bg: '#f3e8ff', fg: '#7c3aed', chipBg: '#e9d5ff', chipFg: '#5b21b6' }
      // TX/RX → UART 橙黄系
      if (role === 1 || role === 2) return { bg: '#fff7e6', fg: '#d97706', chipBg: '#ffe4b3', chipFg: '#b45309' }
      // GPIO → 绿色系
      if (role === 9) return { bg: '#ecfdf5', fg: '#059669', chipBg: '#d1fae5', chipFg: '#047857' }
      // CHANNEL → 灰色系
      if (role === 10) return { bg: '#f3f4f6', fg: '#4b5563', chipBg: '#e5e7eb', chipFg: '#374151' }
      // default
      return { bg: '#f9f9f9', fg: '#666', chipBg: '#e0e0e0', chipFg: '#555' }
    }

    // 解析单个 pin 条目：支持 {role, pin} 对象 或 "MOSI=10" 字符串
    const resolvePin = (raw: any): { role: number; label: string; num: string } | null => {
      if (!raw) return null

      // 字符串格式: "MOSI=10" 或 "MOSI10" 或 "MOSI_10"
      if (typeof raw === 'string') {
        const s = raw.trim()
        const eqIdx = s.indexOf('=')
        let label: string, numStr: string
        if (eqIdx >= 0) {
          label = s.slice(0, eqIdx)
          numStr = s.slice(eqIdx + 1)
        } else {
          // 尝试从末尾分离数字: MOSI10 → label=MOSI, num=10
          const match = s.match(/^([A-Z_]+)(\d+)$/)
          if (match) {
            label = match[1]
            numStr = match[2]
          } else {
            // 全是字母或无法分割，直接显示原字符串
            return null
          }
        }
        // 把标签转为 role
        const roleMap: Record<string, number> = {
          'TX': 1, 'RX': 2, 'SDA': 3, 'SCL': 4,
          'MOSI': 5, 'MISO': 6, 'SCLK': 7, 'SCK': 7, 'CS': 8,
          'GPIO': 9, 'CH': 10
        }
        const role = roleMap[label.toUpperCase()] ?? 0
        return { role, label: label.toUpperCase(), num: numStr }
      }

      // 对象格式: {role: number, pin: number}
      if (typeof raw === 'object') {
        const role = Number(raw.role) || 0
        const label = PIN_ROLE_LABELS[role] || String(raw.role)
        const num = raw.pin >= 0 ? String(raw.pin) : 'N/A'
        return { role, label, num }
      }

      return null
    }

    return () => {
      if (!props.pins || props.pins.length === 0) return null
      return h('span', { class: 'pin-badges' },
        props.pins.map((raw: any, i: number) => {
          const resolved = resolvePin(raw)
          if (!resolved) return null
          const { role, label, num } = resolved
          const theme = getRoleTheme(role)

          return h('span', {
            key: i,
            class: 'pin-chip',
            style: `
              --pin-bg: ${theme.bg};
              --pin-fg: ${theme.fg};
              --pin-chip-bg: ${theme.chipBg};
              --pin-chip-fg: ${theme.chipFg};
            `
          }, [
            h('span', { class: 'pin-chip-role' }, label),
            h('span', { class: 'pin-chip-num' }, num)
          ])
        }).filter(Boolean)
      )
    }
  }
})

const getHardwareInfo = (hw: any) => {
  const parts: string[] = []
  if (hw.freq_hz) parts.push(`${hw.freq_hz}Hz`)
  if (hw.clock_hz) parts.push(`${hw.clock_hz}Hz`)
  if (hw.baud_rate) parts.push(`${hw.baud_rate}baud`)
  if (hw.direction) parts.push(hw.direction === 'input' ? '输入' : '输出')
  if (hw.mode) parts.push(hw.mode === 'master' ? '主机' : '从机')
  return parts.length > 0 ? `(${parts.join(', ')})` : ''
}

// ============================================================
// DMA 开关逻辑
// ============================================================

const supportsDma = (busType: string): boolean => {
  if (!props.dmaChannels || props.dmaChannels.length === 0) return false
  return ['uart', 'spi', 'i2c'].includes(busType)
}

const busTypeToMask = (busType: string): number => {
  switch (busType) {
    case 'uart': return 1
    case 'i2c': return 2
    case 'spi': return 4
    default: return 0
  }
}

const getDmaForHardware = (busType: string, hw: any): DmaChannelInfo[] => {
  if (!props.dmaChannels) return []
  const busMask = busTypeToMask(busType)
  return props.dmaChannels.filter(dma => (dma.compatible_bus & busMask) !== 0)
}

/** Check if a DMA channel is bound to this specific hardware resource */
const isDmaBoundTo = (dma: DmaChannelInfo, busType: string, hw: any): boolean => {
  if (dma.state === 2) return false  // disabled = OFF
  if (dma.state !== 1 || !dma.bound_to) return false  // not allocated = OFF
  return isBoundToHardware(dma.bound_to, busType, hw.id)
}

/** Match bound_to string against hardware — canonical format: "busType/hwId" */
const isBoundToHardware = (boundTo: string, busType: string, hwId: string): boolean => {
  return boundTo.toLowerCase() === `${busType}/${hwId.toLowerCase()}`
}

const canToggleDma = (dma: DmaChannelInfo, busType: string, hw: any): boolean => {
  if (!props.collectorStatus || props.collectorStatus !== 'online') return false
  // Already bound to this hardware — can toggle OFF
  if (isDmaBoundTo(dma, busType, hw)) return true
  // Free channel — can toggle ON
  if (dma.state === 0) return true
  // Allocated to another hardware — cannot toggle
  return false
}

// DMA 开关 loading 状态（按 dma_id 跟踪）
const dmaToggling = ref<Record<number, boolean>>({})

// DMA 开关乐观更新覆盖层（key格式: `${busType}/${hw.id}/${dma.dma_id}`）
const dmaOverrideMap = ref<Record<string, boolean>>({})

const getDmaSwitchModel = (busType: string, hw: any, dma: DmaChannelInfo): boolean => {
  const key = `${busType}/${hw.id}/${dma.dma_id}`
  if (key in dmaOverrideMap.value) {
    return dmaOverrideMap.value[key]
  }
  return isDmaBoundTo(dma, busType, hw)
}

const toggleDmaForHardware = async (busType: string, hw: any, dma: DmaChannelInfo, enabled: boolean) => {
  const overrideKey = `${busType}/${hw.id}/${dma.dma_id}`
  // 乐观更新：立即设置 override 为目标值
  dmaOverrideMap.value[overrideKey] = enabled
  dmaToggling.value[dma.dma_id] = true
  try {
    // Standardize bind_to format: bus_type/hw_id (e.g. "uart/UART1", "spi/SPI2")
    const bindTo = enabled ? `${busType.toLowerCase()}/${hw.id}` : ''
    await nodeApi.updateDmaConfig(props.collectorId, [{
      dma_id: dma.dma_id,
      enabled: enabled,
      bind_to: bindTo
    }])
    // API成功后刷新总线配置以同步硬件绑定状态
    await refreshBuses()
    // refreshBuses 完成后会清除所有 override
    ElMessage.success(enabled ? `DMA ${dma.name} 已启用` : `DMA ${dma.name} 已禁用`)
  } catch (error: any) {
    // API 失败则回滚：删除 override，恢复为 isDmaBoundTo 的真实值
    delete dmaOverrideMap.value[overrideKey]
    ElMessage.error('DMA配置保存失败: ' + (error.message || '未知错误'))
  } finally {
    dmaToggling.value[dma.dma_id] = false
  }
}

// 删除通道
const handleDeleteChannel = async (channelId: number) => {
  try {
    await channelApi.delete(channelId)
    ElMessage.success('通道已删除')
    refreshChannels()
  } catch (error: any) {
    ElMessage.error('删除失败: ' + (error.message || '未知错误'))
  }
}

// 提交改波特率
const submitReconfigure = async () => {
  if (!reconfigureForm.channelId) {
    ElMessage.warning('请选择要配置的通道')
    return
  }
  reconfigureLoading.value = true
  try {
    await channelApi.reconfigure(reconfigureForm.channelId, reconfigureForm.baudrate)
    ElMessage.success('重配置命令已发送')
    reconfigureDialogVisible.value = false
  } catch (error: any) {
    ElMessage.error('重配置失败: ' + (error.message || '未知错误'))
  } finally {
    reconfigureLoading.value = false
  }
}

// 新建通道
const handleOpenChannelManager = (channel?: any, busType?: string, hardwareId?: string) => {
  editingChannelData.value = channel || null
  presetHardwareType.value = busType || ''
  presetHardwareId.value = hardwareId ? String(hardwareId) : ''
  channelManagerVisible.value = true
}

onMounted(() => {
  loadConfigTemplates()
  refreshBuses()
  refreshChannels()
})

defineExpose({
  refreshChannels,
  refreshBuses
})
</script>

<style scoped>
.bus-config-panel {
  padding: 0;
}

.config-section {
  padding: 16px 0;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  gap: 12px;
  padding-bottom: 14px;
  border-bottom: 1px solid #f0f0f0;
}

.section-header h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: #1a1a2e;
  letter-spacing: 0.3px;
}

.header-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.header-actions .el-button {
  border-radius: 8px;
  font-weight: 500;
}

.bus-type-section {
  margin-bottom: 24px;
  padding: 16px;
  background: #f5f7fa;
  border-radius: 8px;
}

.bus-type-header {
  display: flex;
  align-items: center;
  gap: 10px;
}

.bus-type-header .el-tag {
  font-weight: 600;
  letter-spacing: 0.5px;
}

.bus-type-name {
  color: #303133;
  font-size: 14px;
  font-weight: 500;
}

.bus-count-badge {
  color: #909399;
  font-size: 12px;
  background: #f4f4f5;
  padding: 1px 8px;
  border-radius: 10px;
}

.bus-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.bus-item {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  background: white;
  border-radius: 4px;
  flex-wrap: wrap;
  gap: 4px;
}

.uart-item {
  flex-wrap: wrap;
}

.pin-badges {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 5px;
}

/* 两段式引脚徽章 */
.pin-chip {
  display: inline-flex;
  align-items: stretch;
  border-radius: 6px;
  overflow: hidden;
  font-family: 'JetBrains Mono', 'Cascadia Code', 'Courier New', Consolas, monospace;
  font-size: 11px;
  font-weight: 600;
  background: var(--pin-bg);
  border: 1px solid transparent;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  transition: box-shadow 0.2s, transform 0.15s;
  cursor: default;
}

.pin-chip:hover {
  box-shadow: 0 2px 6px rgba(0, 0, 0, 0.14);
  transform: translateY(-1px);
}

/* 左段：角色名（SDA/SCL/MOSI...）*/
.pin-chip-role {
  padding: 2px 6px;
  background: var(--pin-chip-bg);
  color: var(--pin-chip-fg);
  letter-spacing: 0.4px;
  border-right: 1px solid rgba(0, 0, 0, 0.08);
  white-space: nowrap;
}

/* 右段：引脚编号（8/9/10...）*/
.pin-chip-num {
  padding: 2px 7px;
  color: var(--pin-fg);
  white-space: nowrap;
  min-width: 22px;
  text-align: center;
}

.param-hint {
  color: #909399;
  font-size: 12px;
  margin-left: 4px;
}

.tip-box {
  margin-top: 16px;
  padding: 12px;
  background: #f0f9eb;
  border-radius: 6px;
  border-left: 4px solid #67c23a;
}

.tip-box p {
  margin: 0;
  color: #606266;
  font-size: 13px;
}

.data-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.data-item {
  padding: 12px;
  background: #f5f7fa;
  border-radius: 6px;
}

.data-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.data-time {
  color: #909399;
  font-size: 12px;
}

.data-content {
  display: flex;
  align-items: center;
  gap: 8px;
}

.data-label {
  color: #606266;
  font-size: 13px;
}

.data-raw {
  padding: 4px 8px;
  background: #e6f7ff;
  border-radius: 4px;
  font-family: monospace;
  font-size: 12px;
}

.assoc-hint {
  color: #909399;
  font-size: 12px;
  margin-left: 8px;
}

/* ---- Bus Group ---- */
.channel-bus-group {
  margin-bottom: 16px;
}

/* collapse 样式优化 */
.channel-bus-group :deep(.el-collapse) {
  border: none;
  border-radius: 10px;
  overflow: hidden;
  background: #fff;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06);
}
.channel-bus-group :deep(.el-collapse-item__header) {
  background: linear-gradient(135deg, #f8f9fb 0%, #f0f2f5 100%);
  border-bottom: 1px solid #ebeef5;
  padding: 10px 16px;
  font-weight: normal;
  transition: background 0.2s;
}
.channel-bus-group :deep(.el-collapse-item__header:hover) {
  background: linear-gradient(135deg, #eef2f8 0%, #e6ebf2 100%);
}
.channel-bus-group :deep(.el-collapse-item__wrap) {
  background: transparent;
  border-bottom: none;
}
.channel-bus-group :deep(.el-collapse-item__content) {
  padding: 12px 16px 8px 16px;
}
.channel-bus-group :deep(.el-collapse-item__arrow) {
  color: #909399;
  transition: transform 0.3s;
}
.channel-bus-group :deep(.el-collapse-item.is-disabled .el-collapse-item__header) {
  color: #c0c4cc;
  cursor: not-allowed;
}

/* ---- Hardware Card ---- */
.hardware-channel-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.hardware-channel-item {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 16px;
  background: #fafbfc;
  border-radius: 10px;
  border: 1px solid #e6eaef;
  transition: all 0.25s cubic-bezier(0.4, 0, 0.2, 1);
  position: relative;
  overflow: hidden;
}

.hardware-channel-item::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 3px;
  background: #d0d5dd;
  border-radius: 3px 0 0 3px;
  transition: background 0.2s;
}

.hardware-channel-item:hover {
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
  transform: translateY(-1px);
}

/* 有通道：左侧品牌色 + 浅绿底 */
.hardware-channel-item.has-channel {
  background: #f6fbf2;
  border-color: #c5deb4;
}
.hardware-channel-item.has-channel::before {
  background: #67c23a;
}

/* 无通道：左侧灰色 + 虚线边框 */
.hardware-channel-item:not(.has-channel) {
  background: #fafafa;
  border-style: dashed;
  border-color: #d5dce3;
}
.hardware-channel-item:not(.has-channel)::before {
  background: #c0c4cc;
}

/* 有通道：浅绿色背景 + 实线边框 */
.hardware-channel-item.has-channel {
  background: #f0f9eb;
  border-color: #b3e09b;
}

/* 无通道：浅灰背景 + 虚线边框 */
.hardware-channel-item:not(.has-channel) {
  background: #f9f9f9;
  border-style: dashed;
  border-color: #dcdfe6;
}

/* 第一行：名称 + 类型 + 参数 */
.hardware-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}

.hardware-main {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  flex: 1;
}

.hardware-id {
  font-weight: 600;
  color: #1a1a2e;
  font-size: 14px;
  letter-spacing: 0.2px;
}

.hardware-info {
  color: #909399;
  font-size: 12px;
}

/* 启用 checkbox 美化 */
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled.is-checked .el-checkbox__inner) {
  background-color: #67c23a;
  border-color: #67c23a;
}
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled.is-checked .el-checkbox__inner::after) {
  border-color: #fff;
}
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled .el-checkbox__inner) {
  background-color: #ebeeef;
  border-color: #dcdfe6;
}

/* DMA 开关样式 */
.hardware-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}

.dma-switch {
  --el-switch-on-color: #409eff;
  --el-switch-off-color: #dcdfe6;
}

.dma-switch :deep(.el-switch__label) {
  font-size: 11px;
  font-family: 'Courier New', monospace;
  color: #606266;
}

.dma-switch :deep(.el-switch__label.is-active) {
  color: #409eff;
}

/* 第二行：引脚 */


/* 第三行：通道 */
.hardware-card-channels {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 26px;
}

.channel-tag {
  cursor: pointer;
  border-radius: 8px;
  font-weight: 600;
  transition: all 0.2s;
  padding: 4px 10px;
  border-color: #409eff;
  background: #ecf5ff;
  color: #1d60d6;
}

.channel-tag:hover {
  opacity: 0.88;
  background: #d9ecff;
  transform: scale(1.03);
}

.channel-tag-name {
  font-size: 13px;
  font-weight: 600;
  margin-right: 4px;
}

.channel-tag-id {
  font-size: 11px;
  font-weight: 500;
  opacity: 0.7;
  margin-left: 2px;
}

/* 无通道提示 */
.no-channel-hint {
  display: flex;
  align-items: center;
}

.no-channel-dashed {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 5px 14px;
  border: 1px dashed #c8cdd6;
  border-radius: 20px;
  background: rgba(255, 255, 255, 0.5);
  transition: all 0.2s;
}

.no-channel-dashed:hover {
  border-color: #409eff;
  background: rgba(64, 158, 255, 0.04);
}

.no-channel-text {
  color: #b0b8c4;
  font-size: 12px;
}

.add-channel-btn {
  font-size: 12px;
  padding: 0;
  height: auto;
  color: #409eff;
  font-weight: 500;
}

.add-channel-btn:hover {
  color: #66b1ff;
}



/* ---- ChannelTerminal (shared, but scoped here) ---- */
.terminal-controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding: 12px;
  background: #f5f7fa;
  border-radius: 6px;
}

.terminal-log {
  max-height: 400px;
  overflow-y: auto;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  background: #1e1e1e;
  font-family: 'Courier New', monospace;
  font-size: 13px;
}

.terminal-entry {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 6px 12px;
  border-bottom: 1px solid #2d2d2d;
}

.terminal-entry:last-child {
  border-bottom: none;
}

.terminal-entry.send {
  color: #4fc3f7;
}

.terminal-entry.recv {
  color: #a5d6a7;
}

.terminal-entry.error {
  color: #ef5350;
}

.terminal-direction {
  font-weight: bold;
  min-width: 28px;
  color: inherit;
}

.terminal-time {
  color: #888;
  font-size: 11px;
  min-width: 70px;
}

.terminal-data {
  color: #e0e0e0;
  word-break: break-all;
  background: transparent;
  padding: 0;
  font-size: 13px;
}

.terminal-error {
  color: #ef5350;
  font-size: 12px;
}
</style>
