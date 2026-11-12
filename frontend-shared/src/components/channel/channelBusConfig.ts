import type { ChannelFormState, HardwareResource } from './channelFormModel'

/**
 * 通道提交载荷中的「总线参数」域：address 归一、config JSON、bus_config hex 字节编码。
 *
 * 边界依据：这三件事是纯函数式的字节/字段换算，不碰 store、事务代际或 props 契约；
 * 抽成工具模块后，ChannelManager.vue 只保留「何时提交、写哪个 store、如何同步节点」。
 * 字节布局与固件权威定义、backend channel_reconfigure.go 的 build*BusConfig 逐字节一致，
 * 没有改动任何一位 —— 这里只是把原实现原样搬家。
 */

export interface ChannelSubmitBusPayload {
  /** 提交给后端的 address（SPI 用 cs_pin 作为地址，与原实现一致）。 */
  address: string
  /** 提交给后端的 config JSON 对象（UART 会补回可读的帧格式字段）。 */
  config: Record<string, any>
  /** 总线配置 hex；组装不出合法值时为**空串**（调用方负责决定是否带该字段）。 */
  busConfig: string
}

/** 与原先三处 Array.from(...).map(...).join('').toUpperCase() 完全同口径。 */
const toHex = (buf: Uint8Array): string =>
  Array.from(buf).map(b => b.toString(16).padStart(2, '0')).join('').toUpperCase()

/**
 * 组装提交字段。hw 为 undefined 时（能力未上报）保持空 busConfig ——
 * 是否允许这种提交由 ChannelManager.vue 的提交前守卫决定，本函数不改变该判定。
 */
export function buildChannelSubmitBusPayload(
  form: ChannelFormState,
  hw: HardwareResource | undefined,
  defaultUartBaudRate: number,
): ChannelSubmitBusPayload {
  // 处理地址
  let address = form.address
  if (form.hardware_type === 'spi') {
    address = String(form.config.cs_pin ?? 0)
  }

  // 组装 config
  const config: Record<string, any> = { ...form.config }

  // 组装 bus_config (硬件总线配置的hex字节数组)
  let busConfig = ''
  if (form.hardware_type === 'uart' && hw) {
    const tx_pin = form.config.tx_pin || hw.default_tx_pin
    const rx_pin = form.config.rx_pin || hw.default_rx_pin
    // 兜底同样是 9600 而非 115200（见 ChannelManager.vue 的 DEFAULT_UART_BAUD_RATE 说明）。
    // 默认值由调用方显式传入，不在本模块再写一份，避免表单默认值与提交兜底值漂移。
    const baud = form.config.baud_rate || defaultUartBaudRate
    // 帧格式参数不再进 bus_config（见下面的布局说明）：固件把 UART 硬编码为
    // 8N1，byte 7..9 从不读。它们只写进 config JSON 供人阅读与回显。
    const data_bits = form.config.data_bits || 8
    const stop_bits = form.config.stop_bits || 1
    // bus_config 布局（7 字节）——与固件权威定义和 backend
    // channel_reconfigure.go 的 buildUARTBusConfig 逐字节一致：
    //   [0]=tx_pin [1]=rx_pin [2-5]=baud(BE) [6]=DMA flags（bit0=使能）
    //
    // 为什么不再是 10 字节 [6]=data_bits：固件 bus_dma.h 把 UART 的 bus_config[6]
    // 读作 DMA flags（& 0x01），而 uart_config_t 的帧格式是硬编码 8N1（bus_dma.c），
    // byte 7..9 根本不读。旧写法把 data_bits=8 写进 byte 6 ⇒ 0x08 & 0x01 == 0
    // ⇒ 静默关闭 DMA。帧格式仍进 config JSON（下面几行），只是不编进 bus_config。
    const buf = new Uint8Array(7)
    buf[0] = tx_pin
    buf[1] = rx_pin
    buf[2] = (baud >> 24) & 0xFF
    buf[3] = (baud >> 16) & 0xFF
    buf[4] = (baud >> 8) & 0xFF
    buf[5] = baud & 0xFF
    // ⚠⚠ 2026-10-09（用户明确要求）：**DMA 默认都不开，由用户手动配置。**
    //   用户原话："C6 S3的所有UART同时都只有有一个能用DMA！！！"
    //           "修改原则：DMA默认都不开，由用户手动配置"
    //
    // 原为 0x01（默认开）。在 S3/C6 上必然出事：两个型号的 UART 同时只有
    // 1 条能用 DMA（S3 也只有一条 UHCI）；建 2 条 UART 通道就都带着"要 DMA"
    // 的意愿 ⇒ 第 2 条抢不到 ⇒ 设备侧资源计划拒绝整份 manifest
    // （§211 的 C6 现场事故：2818 次 config failed、设备 ch=0 永远 syncing）。
    // ⇒ 改为 0x00。用户要开 DMA 时走 DMA 开关（uartDMAFlagsByte 是唯一构造点）。
    buf[6] = 0x00
    busConfig = toHex(buf)
    // Also store in config JSON for readability
    config.baud_rate = baud
    config.data_bits = data_bits
    config.stop_bits = stop_bits
    config.parity = form.config.parity || 'none'
    config.flow_control = form.config.flow_control || 'none'
  } else if (form.hardware_type === 'i2c' && hw) {
    const sda = form.config.sda_pin || hw.default_sda_pin
    const scl = form.config.scl_pin || hw.default_scl_pin
    const addr = parseInt(form.address, 16) || 0
    const freq = form.config.freq_hz || hw.max_freq_hz || 400000
    const buf = new Uint8Array(7)
    buf[0] = sda; buf[1] = scl; buf[2] = addr & 0xFF
    buf[3] = (freq >> 24) & 0xFF; buf[4] = (freq >> 16) & 0xFF
    buf[5] = (freq >> 8) & 0xFF; buf[6] = freq & 0xFF
    busConfig = toHex(buf)
  } else if (form.hardware_type === 'spi' && hw) {
    const cs = form.config.cs_pin || hw.default_cs_pin || 5
    const mode = form.config.spi_mode || 0
    const freq = form.config.freq_hz || hw.max_freq_hz || 1000000
    const mosi = form.config.mosi_pin || hw.default_mosi || -1
    const miso = form.config.miso_pin || hw.default_miso || -1
    const sclk = form.config.sclk_pin || hw.default_sclk || -1
    // bus_config: [cs(1B)][mode(1B)][freq(4B BE)][mosi(1B)][miso(1B)][sclk(1B)]
    const buf = new Uint8Array(9)
    buf[0] = cs; buf[1] = mode
    buf[2] = (freq >> 24) & 0xFF; buf[3] = (freq >> 16) & 0xFF
    buf[4] = (freq >> 8) & 0xFF; buf[5] = freq & 0xFF
    buf[6] = mosi; buf[7] = miso; buf[8] = sclk
    busConfig = toHex(buf)
  }

  return { address, config, busConfig }
}
