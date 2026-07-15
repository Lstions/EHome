<template>
  <div class="bus-config-panel">
    <el-tabs v-model="activeTab">
      <el-tab-pane label="硬件资源" name="channels">
        <div class="config-section">
          <div class="section-header">
            <div class="header-actions">
              <el-button size="small" @click="refreshAll" :loading="loading || channelsLoading">
                <el-icon><Refresh /></el-icon>
                刷新
              </el-button>
              <el-button size="small" type="primary" @click="saveBusConfig" :loading="saving" :disabled="collectorStatus !== 'online'">
                保存配置
              </el-button>
              <el-button type="primary" size="small" @click="handleOpenChannelManager()" :disabled="collectorStatus !== 'online'">
                <el-icon><Plus /></el-icon>
                新建通道
              </el-button>
            </div>
          </div>

          <!-- 加载中骨架屏 -->
          <template v-if="contentLoading">
            <el-skeleton :rows="8" animated />
          </template>

          <!-- 加载完成后的内容 -->
          <template v-else>

          <!-- 按硬件类型分组展示: 通信总线 -->
          <div v-for="busType in ['i2c', 'uart', 'spi', 'adc']" :key="busType" class="channel-bus-group">
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
                          <template v-if="supportsDma(busType)">
                            <el-switch
                              v-for="dma in getDmaForHardware(busType, hw)"
                              :key="dma.dma_id"
                              :model-value="dmaStore.isSwitchOn(dma) && isDmaBoundTo(dma, busType, hw)"
                              :disabled="!canToggleDma(dma, busType, hw)"
                              :loading="dmaStore.toggling[dma.dma_id] || false"
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
                          <el-button
                            v-if="busType === 'uart' && getChannelsForHardware(busType, hw.id).length > 0 && collectorStatus === 'online'"
                            size="small"
                            text
                            type="warning"
                            @click="openReconfigure(getChannelsForHardware(busType, hw.id)[0])"
                          >
                            改波特率
                          </el-button>
                          <el-button
                            v-if="isScannable(busType, hw)"
                            size="small"
                            type="warning"
                            :loading="scanningHwId === hw.id"
                            @click="handleScan(busType, hw)"
                            class="scan-btn"
                          >
                            地址扫描
                          </el-button>
                        </template>
                        <template v-else>
                          <div class="no-channel-hint">
                            <span class="no-channel-dashed">
                              <span class="no-channel-text">无通道</span>
                              <el-button
                                type="primary"
                                text
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

          <!-- GPIO / PWM 引脚资源 — 行式控制面板 -->
          <div v-if="hardware.gpio?.length > 0 || gpioConfigs.length > 0 || pwmConfigs.length > 0" class="channel-bus-group">
            <el-collapse>
              <el-collapse-item>
                <template #title>
                  <div class="bus-type-header">
                    <el-tag :type="getBusTagType('gpio')" size="small">GPIO/PWM</el-tag>
                    <span class="bus-type-name">GPIO / PWM 引脚</span>
                    <span class="bus-count-badge">共 {{ hardware.gpio?.length || 0 }} · 已配置 {{ gpioConfigs.length + pwmConfigs.length }} · 可用 {{ availablePinCount }}</span>
                  </div>
                </template>
                <PinResourceList
                  :hardware-gpio="hardware.gpio || []"
                  :gpio-configs="gpioConfigs"
                  :pwm-configs="pwmConfigs"
                  :node-id="nodeDeviceId || ''"
                  :offline="collectorStatus !== 'online'"
                  :initial-loading="periphLoading && gpioConfigs.length === 0 && pwmConfigs.length === 0"
                  :refreshing="periphLoading"
                  :occupied-pins="occupiedPinMap"
                  @configure-gpio="openGpioDialogFromRow"
                  @configure-pwm="openPwmDialogFromRow"
                  @edit-gpio="openEditGpioDialog"
                  @edit-pwm="openEditPwmDialog"
                  @remove-gpio="handleRemoveGpio"
                  @remove-pwm="handleRemovePwm"
                  @refresh="refreshPeriph"
                  @retry="refreshPeriph"
                  @row-updated="refreshPeriph"
                />
              </el-collapse-item>
            </el-collapse>
          </div>

          <el-empty v-if="allChannels.length === 0 && gpioConfigs.length === 0 && pwmConfigs.length === 0 && !channelsLoading" description="暂无硬件资源" />
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

    <!-- 添加 GPIO 对话框 -->
    <el-dialog v-model="gpioDialogVisible" title="配置 GPIO 引脚" width="460px" destroy-on-close>
      <el-form :model="gpioForm" label-width="90px">
        <el-form-item label="引脚">
          <el-input :model-value="`GPIO${gpioForm.pin}`" disabled style="width: 100%;" />
        </el-form-item>
        <el-form-item label="方向">
          <el-select v-model="gpioForm.direction" style="width: 100%;">
            <el-option :value="1" label="输出 (OUTPUT)" />
            <el-option :value="0" label="输入 (INPUT)" />
            <el-option :value="2" label="上拉输入 (INPUT_PULLUP)" />
            <el-option :value="3" label="下拉输入 (INPUT_PULLDOWN)" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="gpioForm.direction === 1" label="初始电平">
          <el-radio-group v-model="gpioForm.initial_level">
            <el-radio :value="0">低电平 (0)</el-radio>
            <el-radio :value="1">高电平 (1)</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="标签">
          <el-input v-model="gpioForm.label" placeholder="可选，如：继电器" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="gpioDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="gpioSaving" @click="submitGpio">确认添加</el-button>
      </template>
    </el-dialog>

    <!-- 添加 PWM 对话框 -->
    <el-dialog v-model="pwmDialogVisible" title="启用 PWM 输出" width="460px" destroy-on-close>
      <el-form :model="pwmForm" label-width="90px">
        <el-form-item label="引脚">
          <el-input :model-value="`GPIO${pwmForm.pin}`" disabled style="width: 100%;" />
        </el-form-item>
        <el-form-item label="频率 (Hz)">
          <el-input-number v-model="pwmForm.frequency" :min="1" :max="40000000" style="width: 100%;" placeholder="如 1000" />
        </el-form-item>
        <el-form-item label="占空比">
          <el-slider v-model="pwmForm.duty" :min="0" :max="10000" :step="10" show-input :show-tooltip="false" />
          <div class="pwm-duty-hint">0-10000 (0.00% - 100.00%)，当前: {{ (pwmForm.duty / 100).toFixed(2) }}%</div>
        </el-form-item>
        <el-form-item label="分辨率">
          <el-input-number v-model="pwmForm.resolution" :min="4" :max="20" style="width: 100%;" />
          <div class="pwm-duty-hint">4-20 bit，默认 14</div>
        </el-form-item>
        <el-form-item label="自动启动">
          <el-switch v-model="pwmForm.auto_start" />
        </el-form-item>
        <el-form-item label="标签">
          <el-input v-model="pwmForm.label" placeholder="可选" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="pwmDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="pwmSaving" @click="submitPwm">确认添加</el-button>
      </template>
    </el-dialog>

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, onUnmounted, computed, defineComponent, h, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, Plus } from '@element-plus/icons-vue'
import { nodeApi } from '@/api/node'
import { deviceConfigApi } from '@/api/deviceConfig'
import { channelApi } from '@/api/channel'
import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '@/api/periph'
import ChannelManager from '@/components/channel/ChannelManager.vue'
import ChannelTerminal from '@/components/channel/ChannelTerminal.vue'
import PinResourceList from '@/components/periph/PinResourceList.vue'
import { logger } from '@/utils/logger'
import { useDmaStore } from '@/stores/dma'
import { useChannelStore } from '@/stores/channel'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { DmaState, isDmaRebindable } from '@/utils/dmaState'
import type { DmaChannelInfo } from '@/api/node'

interface Props {
  collectorId: number | string
  nodeDeviceId?: string   // Device UUID like "404CCAFFFE57"
  collectorStatus?: string
  dmaChannels?: DmaChannelInfo[]
}

const props = defineProps<Props>()
const dmaStore = useDmaStore()
const channelStore = useChannelStore()

const activeTab = ref('channels')
const initialLoadingDone = ref(false)
const loading = ref(false)
const channelsLoading = ref(false)
const busesLoaded = ref(false)
const channelsLoaded = ref(false)
let panelGeneration = 0
let busesRequestSequence = 0
let channelsRequestSequence = 0
let templatesRequestSequence = 0

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

const openReconfigure = (channel: any) => {
  reconfigureForm.channelId = channel.id
  // Pre-fill current baud rate from channel config
  try {
    const cfg = typeof channel.config === 'string' ? JSON.parse(channel.config) : channel.config
    if (cfg?.baud_rate) {
      reconfigureForm.baudrate = cfg.baud_rate
    }
  } catch { /* ignore */ }
  reconfigureDialogVisible.value = true
}

// 地址扫描
const scanningHwId = ref<string | null>(null)

function isScannable(busType: string, hw: any): boolean {
  if (busType === 'i2c') return true
  if (busType === 'uart') {
    // bus_config may be raw hex or JSON with mode field
    try {
      const cfg = typeof hw.bus_config === 'string' ? JSON.parse(hw.bus_config) : hw.bus_config
      if (cfg?.mode === 'rs485') return true
    } catch {
      // raw hex — treat as scannable (most UART deployments are RS485)
    }
    return !!hw.bus_config
  }
  return false
}

async function handleScan(busType: string, hw: any) {
  // 找到该硬件资源下的第一个通道作为扫描入口
  const channels = getChannelsForHardware(busType, hw.id)
  if (channels.length === 0) {
    ElMessage.warning('该硬件资源下没有通道，无法扫描')
    return
  }
  const ch = channels[0]
  const collectorId = props.collectorId
  const generation = panelGeneration
  const scanHwId = hw.id
  scanningHwId.value = hw.id
  try {
    const scanType = busType === 'i2c' ? 'i2c' : 'modbus'
    const result = await channelApi.scan(ch.id, { scan_type: scanType })
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
    ElMessage.success(`扫描完成: 发现 ${result.devices?.length || 0} 个设备`)
  } catch (e: any) {
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
    ElMessage.error('扫描失败: ' + (e.message || '未知错误'))
  } finally {
    if (generation === panelGeneration && props.collectorId === collectorId && scanningHwId.value === scanHwId) scanningHwId.value = null
  }
}

// 配置模板列表
const configTemplates = ref<any[]>([])

// 采集器能力
const capabilities = ref<any>(null)

// 默认资源列表（仅用于空状态保底）
const emptyBuses = {
  adc: [] as any[],
  gpio: [] as any[],
  i2c: [] as any[],
  spi: [] as any[],
  uart: [] as any[]
}

const hardware = ref<any>(JSON.parse(JSON.stringify(emptyBuses)))

const allChannels = ref<any[]>([])

// GPIO/PWM 外设配置
const gpioConfigs = ref<GPIOConfig[]>([])
const pwmConfigs = ref<PWMConfig[]>([])
const periphLoading = ref(false)

const getBusTagType = (type: string) => {
  const types: Record<string, string> = {
    adc: 'success',
    i2c: 'warning',
    spi: 'danger',
    uart: 'primary',
    gpio: 'info',
    pwm: 'info',
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
  for (const type of ['adc', 'i2c', 'spi', 'uart']) {
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
  const sequence = ++templatesRequestSequence
  const generation = panelGeneration
  try {
    const result = await deviceConfigApi.getList({ page_size: 100 })
    if (generation !== panelGeneration || sequence !== templatesRequestSequence) return
    configTemplates.value = result.items || result.list || []
  } catch (error: any) {
    if (generation !== panelGeneration || sequence !== templatesRequestSequence) return
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
  const collectorId = props.collectorId
  const sequence = ++busesRequestSequence
  const generation = panelGeneration
  loading.value = true
  try {
    // Step 0: 向采集器下发 QueryResources，等待 ReportResources 回填 DB
    try {
      await nodeApi.queryResources(collectorId)
      // 等采集器上报 ReportResources（通常 1-2 秒内）
      await new Promise(resolve => setTimeout(resolve, 2000))
    } catch (err) {
      logger.warn('QueryResources 下发失败（或采集器离线），直接从 DB 读取', { error: String(err) })
    }

    if (generation !== panelGeneration || sequence !== busesRequestSequence) return

    // Step 1: 获取设备能力（纯 Hardware，不含用户配置）
    const capData = await nodeApi.getCapabilities(collectorId)
    if (generation !== panelGeneration || sequence !== busesRequestSequence) return
    capabilities.value = capData  // 保存到 ref，供 ChannelManager 使用

    // Step 2: 获取用户配置（HardwareConfig）
    const userConfig = await nodeApi.getHardwareConfig(collectorId)
    if (generation !== panelGeneration || sequence !== busesRequestSequence) return

    // Step 3: 先在临时变量中构建合并结果，避免中间状态导致 UI 闪烁
    const mergedHardware: Record<string, any[]> = {}
    const capHardware = capData?.buses || {}
    for (const type of ['adc', 'i2c', 'spi', 'uart', 'gpio']) {
      if (capHardware[type] && Array.isArray(capHardware[type])) {
        mergedHardware[type] = capHardware[type].map((r: any) => {
          // 从实际 DMA 状态初始化 _dmaEnabled/_dmaId（而非硬编码 false）
          let dmaEnabled = false
          let dmaId: number | null = null
          const dmaList = dmaStore.mergedChannels
          if (dmaList && dmaList.length > 0) {
            for (const dma of dmaList) {
              if (dma.bound_to && dma.state === DmaState.ALLOCATED) {  // state=1 = allocated
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
    // refreshBuses 完成后刷新 DMA store 数据，让 UI 回归真实状态
    void dmaStore.fetch(collectorId)
  } catch (error: any) {
    if (generation !== panelGeneration || sequence !== busesRequestSequence) return
    logger.error('获取配置失败', { error: String(error) })
    if (initialLoadingDone.value) {
      ElMessage.warning('获取配置失败，使用空配置')
    }
  } finally {
    if (generation === panelGeneration && sequence === busesRequestSequence) {
      loading.value = false
      busesLoaded.value = true
      checkInitialDone()
    }
  }
}

const saveBusConfig = async () => {
  const collectorId = props.collectorId
  const generation = panelGeneration
  const sessionGeneration = getSessionGeneration()
  saving.value = true
  try {
    await nodeApi.updateHardwareConfig(collectorId, hardware.value)
    assertSessionGeneration(sessionGeneration)
    if (generation !== panelGeneration || props.collectorId !== collectorId) throw new Error('节点已变更')
    ElMessage.success('总线配置已保存')
  } catch (error: any) {
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
    ElMessage.error('保存失败: ' + (error.message || '未知错误'))
  } finally {
    if (generation === panelGeneration && props.collectorId === collectorId) saving.value = false
  }
}

const refreshChannels = async () => {
  const nodeId = props.nodeDeviceId || props.collectorId
  const sequence = ++channelsRequestSequence
  const generation = panelGeneration
  channelsLoading.value = true
  try {
    const result = await channelApi.getList(nodeId)
    if (generation !== panelGeneration || sequence !== channelsRequestSequence) return
    // Handle both array and {items: []} response
    allChannels.value = Array.isArray(result) ? result : (result.items || [])
    if (initialLoadingDone.value) {
      ElMessage.success('通道列表已刷新')
    }
  } catch (error: any) {
    if (generation !== panelGeneration || sequence !== channelsRequestSequence) return
    logger.error('获取通道列表失败', { error: String(error) })
    allChannels.value = []
  } finally {
    if (generation === panelGeneration && sequence === channelsRequestSequence) {
      channelsLoading.value = false
      channelsLoaded.value = true
      checkInitialDone()
    }
  }
}

// 加载 GPIO/PWM 外设配置
const refreshPeriph = async () => {
  if (!props.nodeDeviceId) return
  periphLoading.value = true
  try {
    const [gpios, pwms] = await Promise.all([
      gpioApi.list(props.nodeDeviceId).catch(() => []),
      pwmApi.list(props.nodeDeviceId).catch(() => []),
    ])
    gpioConfigs.value = gpios
    pwmConfigs.value = pwms
  } finally {
    periphLoading.value = false
  }
}

// 统一刷新所有硬件资源
const refreshAll = async () => {
  await Promise.all([refreshBuses(), refreshChannels(), refreshPeriph()])
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
      if (role === 3 || role === 4) return { bg: 'var(--el-color-primary-light-9)', fg: 'var(--el-color-primary)', chipBg: 'var(--el-color-primary-light-7)', chipFg: 'var(--el-color-primary-dark-2)' }
      // MOSI/MISO/SCLK/CS → SPI 紫粉系
      if (role >= 5 && role <= 8) return { bg: 'var(--el-color-info-light-9)', fg: 'var(--color-adc)', chipBg: 'var(--el-color-info-light-7)', chipFg: 'var(--color-adc)' }
      // TX/RX → UART 橙黄系
      if (role === 1 || role === 2) return { bg: 'var(--el-color-warning-light-9)', fg: 'var(--el-color-warning)', chipBg: 'var(--el-color-warning-light-7)', chipFg: 'var(--el-color-warning-dark-2)' }
      // GPIO → 绿色系
      if (role === 9) return { bg: 'var(--el-color-success-light-9)', fg: 'var(--el-color-success)', chipBg: 'var(--el-color-success-light-7)', chipFg: 'var(--el-color-success-dark-2)' }
      // CHANNEL → 灰色系
      if (role === 10) return { bg: 'var(--bg-color-secondary)', fg: 'var(--text-color-regular)', chipBg: 'var(--hover-bg)', chipFg: 'var(--text-color-primary)' }
      // default
      return { bg: 'var(--bg-color-secondary)', fg: 'var(--text-color-regular)', chipBg: 'var(--hover-bg)', chipFg: 'var(--text-color-primary)' }
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
// DMA 开关逻辑 — 统一由 dmaStore 管理
// ============================================================

const supportsDma = (busType: string): boolean => {
  if (dmaStore.mergedChannels.length === 0) return false
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

const getDmaForHardware = (busType: string, _hw: any): DmaChannelInfo[] => {
  const src = dmaStore.mergedChannels
  if (!src || src.length === 0) return []
  const busMask = busTypeToMask(busType)
  return src.filter(dma => (dma.compatible_bus & busMask) !== 0)
}

/** Check if a DMA channel is bound to this specific hardware resource.
 * v2.5: bind state determined by bound_to string, NOT by dma.state.
 * bound_to is the canonical binding reference — if it names this hardware,
 * the toggle should reflect ON regardless of state. */
const isDmaBoundTo = (dma: DmaChannelInfo, busType: string, hw: any): boolean => {
  if (!dma.bound_to) return false
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
  // Free, disabled, or allocated-but-unbound (orphan) — can toggle ON
  // bound_to may be undefined (field missing) or "" (empty string) — !boundTo handles both
  if (isDmaRebindable(dma.state, dma.bound_to)) {
    // v2.5: mutual exclusion — only one DMA per hardware.
    // If another DMA is already bound to this same hw, block.
    const hwKey = `${busType.toLowerCase()}/${hw.id}`
    const allDmas = dmaStore.mergedChannels
    for (const other of allDmas) {
      if (other.dma_id === dma.dma_id) continue
      if (other.bound_to && other.bound_to.toLowerCase() === hwKey) {
        return false  // another DMA already owns this hardware
      }
    }
    return true
  }
  // Allocated and bound to another hardware — cannot toggle
  return false
}

const toggleDmaForHardware = async (busType: string, hw: any, dma: DmaChannelInfo, enabled: boolean) => {
  // Standardize bind_to format: bus_type/hw_id (e.g. "uart/UART1", "spi/SPI2")
  const bindTo = enabled ? `${busType.toLowerCase()}/${hw.id}` : ''
  const collectorId = props.collectorId
  const generation = panelGeneration
  try {
    await dmaStore.toggle(collectorId, dma, enabled, bindTo)
    if (generation !== panelGeneration || props.collectorId !== collectorId) throw new Error('节点已变更')
    ElMessage.success(enabled ? `DMA ${dma.name} 已启用` : `DMA ${dma.name} 已禁用`)
  } catch (error: any) {
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
    ElMessage.error('DMA配置保存失败: ' + (error.message || '未知错误'))
  }
}

// 删除通道
const handleDeleteChannel = async (channelId: number) => {
  const collectorId = props.collectorId
  const generation = panelGeneration
  try {
    await channelStore.deleteChannel(channelId)
    if (generation !== panelGeneration || props.collectorId !== collectorId) throw new Error('节点已变更')
    ElMessage.success('通道已删除')
    refreshChannels()
  } catch (error: any) {
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
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
  const collectorId = props.collectorId
  const generation = panelGeneration
  const sessionGeneration = getSessionGeneration()
  try {
    await channelApi.reconfigure(reconfigureForm.channelId, reconfigureForm.baudrate)
    assertSessionGeneration(sessionGeneration)
    if (generation !== panelGeneration || props.collectorId !== collectorId) throw new Error('节点已变更')
    ElMessage.success('重配置命令已发送')
    reconfigureDialogVisible.value = false
  } catch (error: any) {
    if (generation !== panelGeneration || props.collectorId !== collectorId) return
    ElMessage.error('重配置失败: ' + (error.message || '未知错误'))
  } finally {
    if (generation === panelGeneration && props.collectorId === collectorId) reconfigureLoading.value = false
  }
}

// 新建通道
const handleOpenChannelManager = (channel?: any, busType?: string, hardwareId?: string) => {
  editingChannelData.value = channel || null
  presetHardwareType.value = busType || ''
  presetHardwareId.value = hardwareId ? String(hardwareId) : ''
  channelManagerVisible.value = true
}

// ============================================================
// GPIO/PWM 外设配置 — 添加/删除
// ============================================================

// --- GPIO 对话框 ---
const gpioDialogVisible = ref(false)
const gpioSaving = ref(false)
const gpioForm = reactive({
  pin: 5,
  direction: 1,
  initial_level: 0,
  label: '',
})

// 根据 pin 查找已有的 GPIOConfig
const getGpioConfig = (pin: number): GPIOConfig | undefined => {
  return gpioConfigs.value.find(g => g.pin === pin)
}

// 可用引脚数 = 硬件 GPIO 总数 - 已配置 GPIO - 已配置 PWM
const availablePinCount = computed(() => {
  const total = hardware.value.gpio?.length || 0
  const configured = new Set<number>()
  for (const g of gpioConfigs.value) configured.add(g.pin)
  for (const p of pwmConfigs.value) configured.add(p.pin)
  // 占用映射中的引脚也算非可用
  for (const pin of occupiedPinMap.value.keys()) configured.add(pin)
  return Math.max(0, total - configured.size)
})

// 已占用引脚映射: 从通道列表中推导总线占用
// API 降级: 当前无法精确识别所有 occupied 来源，仅从通道 hardware_type/pins 推导
const occupiedPinMap = computed(() => {
  const map = new Map<number, string>()
  // 遍历通道，从 hardware pins 中提取占用信息
  for (const ch of allChannels.value) {
    if (ch.hardware_type && ch.pins) {
      const pins = Array.isArray(ch.pins) ? ch.pins : []
      for (const p of pins) {
        const pinNum = typeof p === 'object' ? p.pin : (typeof p === 'string' ? parseInt(p.replace(/\D/g, '')) : null)
        if (pinNum != null && !isNaN(pinNum)) {
          const role = typeof p === 'object' ? p.role : 0
          const roleLabels: Record<number, string> = { 1: 'TX', 2: 'RX', 3: 'SDA', 4: 'SCL', 5: 'MOSI', 6: 'MISO', 7: 'SCLK', 8: 'CS' }
          const label = roleLabels[role] ? `${ch.hardware_type?.toUpperCase()} ${roleLabels[role]}` : `${ch.hardware_type?.toUpperCase()} 占用`
          if (!map.has(pinNum)) map.set(pinNum, label)
        }
      }
    }
  }
  return map
})

// PinResourceList 事件处理
const openGpioDialogFromRow = (pin: number) => {
  const hw = hardware.value.gpio?.find(h => h.pin === pin)
  openGpioDialog(pin, hw?.id || `GPIO${pin}`)
}

const openPwmDialogFromRow = (pin: number) => {
  const hw = hardware.value.gpio?.find(h => h.pin === pin)
  openPwmDialog(pin, hw?.id || `GPIO${pin}`)
}

const openEditGpioDialog = (pin: number) => {
  const cfg = getGpioConfig(pin)
  if (cfg) {
    gpioForm.pin = pin
    gpioForm.direction = cfg.direction
    gpioForm.initial_level = cfg.initial_level
    gpioForm.label = cfg.label || ''
    gpioDialogVisible.value = true
  }
}

const openEditPwmDialog = (pin: number) => {
  const cfg = getPwmConfig(pin)
  if (cfg) {
    pwmForm.pin = pin
    pwmForm.frequency = cfg.frequency
    pwmForm.duty = cfg.duty
    pwmForm.resolution = cfg.resolution
    pwmForm.auto_start = cfg.auto_start
    pwmForm.label = cfg.label || ''
    pwmDialogVisible.value = true
  }
}

const openGpioDialog = (pin: number, hwId: string) => {
  gpioForm.pin = pin
  gpioForm.direction = 1
  gpioForm.initial_level = 0
  gpioForm.label = hwId || ''
  gpioDialogVisible.value = true
}

const submitGpio = async () => {
  if (!props.nodeDeviceId) {
    ElMessage.warning('缺少节点 ID')
    return
  }
  gpioSaving.value = true
  try {
    await gpioApi.create(props.nodeDeviceId, {
      pin: gpioForm.pin,
      direction: gpioForm.direction,
      initial_level: gpioForm.initial_level,
      label: gpioForm.label,
    })
    ElMessage.success(`GPIO ${gpioForm.pin} 已添加`)
    gpioDialogVisible.value = false
    await refreshPeriph()
  } catch (e: any) {
    const msg = e?.message || '未知错误'
    if (msg.includes('already configured') || msg.includes('Conflict') || msg.includes('409')) {
      // 该引脚已配置，刷新列表让已配置的卡片显示出来
      ElMessage.warning(`GPIO ${gpioForm.pin} 已存在配置`)
      gpioDialogVisible.value = false
      await refreshPeriph()
    } else {
      ElMessage.error('添加 GPIO 失败: ' + msg)
    }
  } finally {
    gpioSaving.value = false
  }
}

const handleRemoveGpio = async (pin: number) => {
  if (!props.nodeDeviceId) return
  try {
    await gpioApi.delete(props.nodeDeviceId, pin)
    ElMessage.success(`GPIO ${pin} 已删除`)
    refreshPeriph()
  } catch (e: any) {
    ElMessage.error('删除 GPIO 失败: ' + (e?.message || '未知错误'))
  }
}

// --- PWM 对话框 ---
const pwmDialogVisible = ref(false)
const pwmSaving = ref(false)
const pwmForm = reactive({
  pin: 15,
  frequency: 1000,
  duty: 0,
  resolution: 14,
  auto_start: false,
  label: '',
})

// 根据 pin 查找已有的 PWMConfig
const getPwmConfig = (pin: number): PWMConfig | undefined => {
  return pwmConfigs.value.find(p => p.pin === pin)
}

const openPwmDialog = (pin: number, hwId: string) => {
  pwmForm.pin = pin
  pwmForm.frequency = 1000
  pwmForm.duty = 0
  pwmForm.resolution = 14
  pwmForm.auto_start = false
  pwmForm.label = hwId || ''
  pwmDialogVisible.value = true
}

const submitPwm = async () => {
  if (!props.nodeDeviceId) {
    ElMessage.warning('缺少节点 ID')
    return
  }
  pwmSaving.value = true
  try {
    await pwmApi.create(props.nodeDeviceId, {
      pin: pwmForm.pin,
      frequency: pwmForm.frequency,
      duty: pwmForm.duty,
      resolution: pwmForm.resolution,
      auto_start: pwmForm.auto_start,
      label: pwmForm.label,
    })
    ElMessage.success(`PWM GPIO${pwmForm.pin} 已添加`)
    pwmDialogVisible.value = false
    await refreshPeriph()
  } catch (e: any) {
    const msg = e?.message || '未知错误'
    if (msg.includes('already configured') || msg.includes('Conflict') || msg.includes('409')) {
      ElMessage.warning(`PWM GPIO${pwmForm.pin} 已存在配置`)
      pwmDialogVisible.value = false
      await refreshPeriph()
    } else {
      ElMessage.error('添加 PWM 失败: ' + msg)
    }
  } finally {
    pwmSaving.value = false
  }
}

const handleRemovePwm = async (pin: number) => {
  if (!props.nodeDeviceId) return
  try {
    await pwmApi.delete(props.nodeDeviceId, pin)
    ElMessage.success(`PWM ${pin} 已删除`)
    refreshPeriph()
  } catch (e: any) {
    ElMessage.error('删除 PWM 失败: ' + (e?.message || '未知错误'))
  }
}

onMounted(() => {
  loadConfigTemplates()
  refreshBuses()
  refreshChannels()
  refreshPeriph()
})

onUnmounted(() => {
  panelGeneration++
  saving.value = false
  reconfigureLoading.value = false
  scanningHwId.value = null
})

watch(() => [props.collectorId, props.nodeDeviceId] as const, ([newCollector, newDevice], [oldCollector, oldDevice]) => {
  if (newCollector === oldCollector && newDevice === oldDevice) return
  panelGeneration++
  saving.value = false
  reconfigureLoading.value = false
  scanningHwId.value = null
  channelManagerVisible.value = false
  reconfigureDialogVisible.value = false
  editingChannelData.value = null
  initialLoadingDone.value = false
  busesLoaded.value = false
  channelsLoaded.value = false
  hardware.value = { ...emptyBuses }
  allChannels.value = []
  capabilities.value = null
  void loadConfigTemplates()
  void refreshBuses()
  void refreshChannels()
})

defineExpose({
  refreshChannels,
  refreshBuses,
  refreshPeriph,
  handleOpenChannelManager
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
  border-bottom: 1px solid var(--border-color-light);
}

.section-header h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: var(--text-color-primary);
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
  background: var(--el-fill-color-light);
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
  color: var(--text-color-primary);
  font-size: 14px;
  font-weight: 500;
}

.bus-count-badge {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  background: var(--bg-color-secondary);
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
  box-shadow: var(--shadow-sm);
  transition: box-shadow 0.2s, transform 0.15s;
  cursor: default;
}

.pin-chip:hover {
  box-shadow: var(--shadow-md);
  transform: translateY(-1px);
}

/* 左段：角色名（SDA/SCL/MOSI...）*/
.pin-chip-role {
  padding: 2px 6px;
  background: var(--pin-chip-bg);
  color: var(--pin-chip-fg);
  letter-spacing: 0.4px;
  border-right: 1px solid var(--border-color-light);
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
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-left: 4px;
}

.tip-box {
  margin-top: 16px;
  padding: 12px;
  background: var(--el-color-success-light-9);
  border-radius: 6px;
  border-left: 4px solid var(--el-color-success);
}

.tip-box p {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 13px;
}

.data-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.data-item {
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 6px;
}

.data-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.data-time {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.data-content {
  display: flex;
  align-items: center;
  gap: 8px;
}

.data-label {
  color: var(--el-text-color-regular);
  font-size: 13px;
}

.data-raw {
  padding: 4px 8px;
  background: var(--el-color-primary-light-9);
  border-radius: 4px;
  font-family: monospace;
  font-size: 12px;
}

.assoc-hint {
  color: var(--el-text-color-secondary);
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
  background: var(--card-bg);
  box-shadow: var(--shadow-sm);
}
.channel-bus-group :deep(.el-collapse-item__header) {
  background: linear-gradient(135deg, var(--bg-color-secondary) 0%, var(--hover-bg) 100%);
  border-bottom: 1px solid var(--border-color-light);
  padding: 10px 16px;
  font-weight: normal;
  transition: background 0.2s;
}
.channel-bus-group :deep(.el-collapse-item__header:hover) {
  background: linear-gradient(135deg, var(--hover-bg) 0%, var(--bg-color-secondary) 100%);
}
.channel-bus-group :deep(.el-collapse-item__wrap) {
  background: transparent;
  border-bottom: none;
}
.channel-bus-group :deep(.el-collapse-item__content) {
  padding: 12px 16px 8px 16px;
}
.channel-bus-group :deep(.el-collapse-item__arrow) {
  color: var(--el-text-color-secondary);
  transition: transform 0.3s;
}
.channel-bus-group :deep(.el-collapse-item.is-disabled .el-collapse-item__header) {
  color: var(--el-text-color-placeholder);
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
  background: var(--bg-color-secondary);
  border-radius: 10px;
  border: 1px solid var(--border-color);
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
  background: var(--text-color-placeholder);
  border-radius: 3px 0 0 3px;
  transition: background 0.2s;
}

.hardware-channel-item:hover {
  box-shadow: var(--shadow-md);
  transform: translateY(-1px);
}

/* 有通道：左侧品牌色 + 浅绿底 */
.hardware-channel-item.has-channel {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-5);
}
.hardware-channel-item.has-channel::before {
  background: var(--el-color-success);
}

/* 无通道：左侧灰色 + 虚线边框 */
.hardware-channel-item:not(.has-channel) {
  background: var(--bg-color-secondary);
  border-style: dashed;
  border-color: var(--border-color);
}
.hardware-channel-item:not(.has-channel)::before {
  background: var(--el-text-color-placeholder);
}

/* 有通道：浅绿色背景 + 实线边框 */
.hardware-channel-item.has-channel {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-5);
}

/* 无通道：浅灰背景 + 虚线边框 */
.hardware-channel-item:not(.has-channel) {
  background: var(--bg-color-secondary);
  border-style: dashed;
  border-color: var(--border-color);
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
  color: var(--text-color-primary);
  font-size: 14px;
  letter-spacing: 0.2px;
}

.hardware-info {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

/* 启用 checkbox 美化 */
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled.is-checked .el-checkbox__inner) {
  background-color: var(--el-color-success);
  border-color: var(--el-color-success);
}
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled.is-checked .el-checkbox__inner::after) {
  border-color: var(--card-bg);
}
.hw-enabled-checkbox :deep(.el-checkbox__input.is-disabled .el-checkbox__inner) {
  background-color: var(--hover-bg);
  border-color: var(--border-color);
}

/* DMA 开关样式 */
.hardware-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}

.dma-switch {
  --el-switch-on-color: var(--el-color-primary);
  --el-switch-off-color: var(--border-color);
}

.dma-switch :deep(.el-switch__label) {
  font-size: 11px;
  font-family: 'Courier New', monospace;
  color: var(--el-text-color-regular);
}

.dma-switch :deep(.el-switch__label.is-active) {
  color: var(--el-color-primary);
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
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.channel-tag:hover {
  opacity: 0.88;
  background: var(--el-color-primary-light-7);
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
  border: 1px dashed var(--border-color);
  border-radius: 20px;
  background: color-mix(in srgb, var(--card-bg) 50%, transparent);
  transition: all 0.2s;
}

.no-channel-dashed:hover {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}

.no-channel-text {
  color: var(--text-color-placeholder);
  font-size: 12px;
}

.add-channel-btn {
  font-size: 12px;
  padding: 0;
  height: auto;
  color: var(--el-color-primary);
  font-weight: 500;
}

.add-channel-btn:hover {
  color: var(--el-color-primary-light-3);
}

.scan-btn {
  margin-left: 4px;
  border-radius: 8px;
  font-weight: 500;
}



/* ---- ChannelTerminal (shared, but scoped here) ---- */
.terminal-controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 6px;
}

.terminal-log {
  max-height: 400px;
  overflow-y: auto;
  border: 1px solid var(--border-color-light);
  border-radius: 6px;
  background: var(--terminal-bg);
  font-family: 'Courier New', monospace;
  font-size: 13px;
}

.terminal-entry {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 6px 12px;
  border-bottom: 1px solid var(--border-color);
}

.terminal-entry:last-child {
  border-bottom: none;
}

.terminal-entry.send {
  color: var(--terminal-accent);
}

.terminal-entry.recv {
  color: var(--terminal-success);
}

.terminal-entry.error {
  color: var(--terminal-danger);
}

.terminal-direction {
  font-weight: bold;
  min-width: 28px;
  color: inherit;
}

.terminal-time {
  color: var(--terminal-muted);
  font-size: 11px;
  min-width: 70px;
}

.terminal-data {
  color: var(--terminal-text);
  word-break: break-all;
  background: transparent;
  padding: 0;
  font-size: 13px;
}

.terminal-error {
  color: var(--terminal-danger);
  font-size: 12px;
}

.pwm-duty-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 2px;
}
</style>
