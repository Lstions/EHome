import { computed, reactive } from 'vue'
import { toBusType, type ChannelFormState, type HardwareResource } from '../channelFormModel'

/**
 * ChannelManager 的表单状态与「能力 → 默认值」投影。
 *
 * 边界依据（为什么这些能搬、那些不能搬）：
 *   · 搬到这里的都是**不依赖调用方事务**的纯表单工作 —— 表单初值 / 校验规则、
 *     能力清单 → 当前资源能力（含能力上报缺失时的安全兜底）、切换硬件后的配置默认值、
 *     编辑态回填（含 bus_config hex 解码）。
 *   · 提交链路仍留在 ChannelManager.vue：store 写入、会话代际校验、collectorId 绑定
 *     是组件的调用方契约，且 ChannelManager.spec.ts 的源码门禁要求
 *     channelStore.createChannel / updateChannel、assertSessionGeneration(sessionGeneration)、
 *     const collectorId = props.collectorId、assertTransaction(...) 出现在原组件文本中。
 *   · bus_config 的字节编码按域搬到 channelBusConfig.ts；本文件只产出表单语义。
 */

/** 组合式函数只需要这几个表单相关入参，不依赖 ChannelManager 的完整 Props 契约。 */
export interface ChannelManagerFormSource {
  capabilities?: any
  presetHardwareType?: string
  presetHardwareId?: string
}

/**
 * 页面注入的提交契约默认值。
 *
 * UART 默认波特率由 ChannelManager.vue 定义并注入（不在本文件再写一份），
 * 保证「表单默认值」与「提交兜底值」共用同一常量，且源码门禁在原组件文本里仍能命中。
 */
export interface ChannelManagerDefaults {
  defaultUartBaudRate: number
}

export function useChannelManager(source: ChannelManagerFormSource, options: ChannelManagerDefaults) {
  const form = reactive<ChannelFormState>({
    name: '',
    hardware_type: 'i2c',
    hardware_id: '',
    address: '',
    enabled: true,
    config: {},
  })

  const rules = {
    hardware_type: [{ required: true, message: '请选择硬件类型', trigger: 'change' }],
    hardware_id: [{ required: true, message: '请选择硬件资源', trigger: 'change' }],
  }

  // ====== 能力数据 ======

  // 获取当前选中硬件的能力数据。能力上报尚未到达时提供安全兜底，
  // 以确保既有通道在编辑时仍可修改其已保存的参数。
  const currentCaps = computed(() => {
    const hardwareType = form.hardware_type.toLowerCase()
    if (!hardwareType) return null
    const hwList = source.capabilities?.buses?.[hardwareType] || []
    const hw = hwList.find((h: any) => String(h.id) === form.hardware_id)

    // 1) 尝试嵌套结构: hw.Capabilities.UartCaps (Go protobuf JSON)
    const capsWrapper = hw?.Capabilities || hw?.capabilities
    if (capsWrapper) {
      const capsKeyMap: Record<string, string> = {
        uart: 'UartCaps',
        i2c: 'I2CCaps',
        spi: 'SpiCaps',
        adc: 'AdcCaps',
      }
      const capsKey = capsKeyMap[hardwareType]
      const nested = capsWrapper[capsKey] || capsWrapper[capsKey?.toLowerCase()]
      if (nested) return nested
    }

    // 2) 能力尚不可用时使用通用安全范围；保存的通道配置仍可编辑。
    switch (hardwareType) {
      case 'uart':
        return {
          baud_rate_max: hw?.max_baud || 921600,
          baud_rate_min: hw?.min_baud || 1200,
          data_bits_options: hw?.data_bits_options || [5, 6, 7, 8],
          stop_bits_options: hw?.stop_bits_options || [1, 1.5, 2],
          parity_options: hw?.parity_options || ['none', 'odd', 'even'],
          flow_control_options: hw?.flow_control_options || ['none', 'rts_cts', 'xon_xoff'],
        }
      case 'i2c':
        return {
          freq_hz_max: hw?.max_freq_hz || 1000000,
          freq_hz_min: hw?.min_freq_hz || 10000,
          address_min: hw?.address_min ?? 8,
          address_max: hw?.address_max ?? 119,
          supports_scan: hw?.supports_scan ?? false,
          supports_10bit: hw?.supports_10bit || false,
        }
      case 'spi':
        return {
          freq_hz_max: hw?.max_freq_hz || 40000000,
          freq_hz_min: hw?.min_freq_hz || 100000,
          mode_options: hw?.mode_options || [0, 1, 2, 3],
          cs_pins: hw?.cs_pins || (hw?.default_cs_pin != null ? [hw.default_cs_pin] : []),
        }
      case 'gpio':
        return { direction_options: hw?.direction_options || ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN'] }
      case 'adc':
        return {
          attenuation_options: hw?.attenuation_options || [0, 1, 2, 3],
          bit_width_options: hw?.bit_width_options || [9, 10, 11, 12],
        }
      default:
        return null
    }
  })

  // 获取指定类型的硬件列表（完整对象）
  const availableHardwareList = computed<HardwareResource[]>(() => {
    if (!source.capabilities?.buses?.[form.hardware_type]) return []
    return source.capabilities.buses[form.hardware_type] || []
  })

  // ====== 事件处理 ======

  const onHardwareTypeChange = () => {
    form.hardware_id = ''
    form.address = ''
    form.config = {}
    initConfigDefaults()
  }

  const onHardwareIdChange = () => {
    form.address = ''
    form.config = {}
    initConfigDefaults()
  }

  // 根据能力数据设置默认值
  const initConfigDefaults = () => {
    const caps = currentCaps.value
    if (!caps) return

    if (form.hardware_type === 'uart') {
      // 默认 9600，不用 115200（2026-10-03 缺陷 4）：本仓现场 UART 设备是
      // 2400(逆变器)/4800(SN-3001、部分 BMS)/9600(多数 BMS)，没有一台用 115200。
      // 拿设备不支持的速率当默认值，用户建完通道仍连不上，必须回头改波特率 ——
      // 而旧代码在"资源能力未上报"时又会落空 bus_config，于是直接卡死。
      // 与后端 defaultUARTBaudrate 保持一致；超出能力范围时夹到能力边界。
      const maxBaud = Number(caps.baud_rate_max) || 0
      const minBaud = Number(caps.baud_rate_min) || 0
      let baud = options.defaultUartBaudRate
      if (maxBaud > 0 && baud > maxBaud) baud = maxBaud
      if (minBaud > 0 && baud < minBaud) baud = minBaud
      form.config.baud_rate = baud
      form.config.data_bits = caps.data_bits_options?.includes(8) ? 8 : (caps.data_bits_options?.[0] || 8)
      form.config.stop_bits = caps.stop_bits_options?.includes(1) ? 1 : (caps.stop_bits_options?.[0] || 1)
      form.config.parity = caps.parity_options?.includes('none') ? 'none' : (caps.parity_options?.[0] || 'none')
      form.config.flow_control = caps.flow_control_options?.includes('none') ? 'none' : (caps.flow_control_options?.[0] || 'none')
    } else if (form.hardware_type === 'i2c') {
      form.config.freq_hz = caps.freq_hz_max || 400000
    } else if (form.hardware_type === 'spi') {
      form.config.freq_hz = caps.freq_hz_max || 1000000
      form.config.cs_pin = caps.cs_pins?.[0] ?? 0
      form.config.spi_mode = caps.mode_options?.includes(0) ? 0 : (caps.mode_options?.[0] ?? 0)
    } else if (form.hardware_type === 'adc') {
      form.config.attenuation = caps.attenuation_options?.includes(3) ? 3 : (caps.attenuation_options?.[0] ?? 3)
      form.config.bit_width = caps.bit_width_options?.includes(12) ? 12 : (caps.bit_width_options?.[0] ?? 12)
    }
  }

  const resetForm = () => {
    form.name = ''
    form.hardware_type = 'i2c'
    form.hardware_id = ''
    form.address = ''
    form.enabled = true
    form.config = {}
  }

  /**
   * 编辑态回填：把 initialData（API 的 Channel 行）投影到表单。
   *
   * config 可能是 JSON 字符串也可能是对象；UART 的 bus_config 是 hex，
   * 其中脚位/波特率不在 config JSON 里，需要解出来回填（原逻辑原样搬移）。
   */
  const hydrateForm = (channel: any) => {
    form.hardware_type = toBusType(channel.hardware_type)
    form.hardware_id = channel.hardware_id || ''
    form.address = channel.address || ''
    form.name = channel.name || ''
    form.enabled = channel.enabled !== false // enabled is bool, not status string
    // config may be JSON string (from API) or object — normalize to object
    let rawConfig = channel.config || {}
    if (typeof rawConfig === 'string') {
      try { rawConfig = JSON.parse(rawConfig) } catch { rawConfig = {} }
    }
    form.config = { ...rawConfig }
    // Also parse bus_config hex for UART to extract params not in config JSON
    if (form.hardware_type === 'uart' && channel.bus_config && typeof channel.bus_config === 'string') {
      const hex = channel.bus_config
      if (!hex.startsWith('{') && hex.length >= 6) {
        // bus_config hex: [tx_pin(1B)][rx_pin(1B)][baud(4B BE)][...optional extended fields]
        try {
          const tx = parseInt(hex.substring(0, 2), 16)
          const rx = parseInt(hex.substring(2, 4), 16)
          if (!form.config.tx_pin) form.config.tx_pin = tx
          if (!form.config.rx_pin) form.config.rx_pin = rx
          if (hex.length >= 12) {
            const baud = parseInt(hex.substring(4, 12), 16)
            if (!form.config.baud_rate) form.config.baud_rate = baud
          }
        } catch { /* ignore decode errors */ }
      }
    }
    // Fill defaults for UART fields not covered by bus_config
    if (form.hardware_type === 'uart') {
      if (!form.config.data_bits) form.config.data_bits = 8
      if (!form.config.stop_bits) form.config.stop_bits = 1
      if (!form.config.parity) form.config.parity = 'none'
      if (!form.config.flow_control) form.config.flow_control = 'none'
    }
  }

  /** 从硬件卡片「创建通道」入口带入的预选硬件（类型 + 资源 id）。 */
  const applyPreset = () => {
    if (source.presetHardwareType) {
      form.hardware_type = toBusType(source.presetHardwareType)
    }
    if (source.presetHardwareId) {
      form.hardware_id = source.presetHardwareId
    }
  }

  return {
    form,
    rules,
    currentCaps,
    availableHardwareList,
    initConfigDefaults,
    onHardwareTypeChange,
    onHardwareIdChange,
    resetForm,
    hydrateForm,
    applyPreset,
  }
}
