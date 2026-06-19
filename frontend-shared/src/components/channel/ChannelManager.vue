<template>
  <div class="channel-manager">
    <!-- 创建/编辑对话框 -->
    <el-dialog
      v-model="showDialog"
      :title="readonly ? '查看通道' : (editingChannel ? '编辑通道' : '创建通道')"
      width="600px"
      :close-on-click-modal="false"
      class="channel-manager-dialog"
      @closed="resetForm"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" :disabled="readonly">
        <!-- 硬件类型（预选时只读） -->
        <el-form-item label="硬件类型" prop="hardware_type">
          <el-select
            v-model="form.hardware_type"
            placeholder="选择硬件类型"
            style="width: 100%;"
            :disabled="!!presetHardwareType"
            @change="onHardwareTypeChange"
          >
            <el-option label="UART" value="uart" />
            <el-option label="I2C" value="i2c" />
            <el-option label="SPI" value="spi" />
            <el-option label="GPIO" value="gpio" />
            <el-option label="ADC" value="adc" />
          </el-select>
        </el-form-item>

        <!-- 硬件ID（预选时只读） -->
        <el-form-item label="硬件资源" prop="hardware_id">
          <el-select
            v-model="form.hardware_id"
            placeholder="选择硬件资源"
            style="width: 100%;"
            :disabled="!!presetHardwareId"
            @change="onHardwareIdChange"
          >
            <el-option
              v-for="hw in availableHardwareList"
              :key="hw.id"
              :label="hw.name || hw.id"
              :value="String(hw.id)"
            >
              <span>{{ hw.name || hw.id }}</span>
              <span v-if="hw.pins?.length" style="color: #909399; font-size: 12px; margin-left: 8px;">
                ({{ formatPinsBrief(hw.pins) }})
              </span>
            </el-option>
          </el-select>
        </el-form-item>

        <!-- ====== 能力驱动的配置参数 ====== -->

        <!-- UART 参数 -->
        <template v-if="form.hardware_type === 'uart' && currentCaps">
          <el-form-item label="波特率">
            <el-input-number
              v-model="form.config.baud_rate"
              :min="currentCaps.baud_rate_min || 1200"
              :max="currentCaps.baud_rate_max || 921600"
              :step="computeBaudStep()"
              style="width: 100%;"
            />
            <div class="cap-hint" v-if="currentCaps.baud_rate_min">
              范围: {{ currentCaps.baud_rate_min }} ~ {{ currentCaps.baud_rate_max }}
            </div>
          </el-form-item>
          <el-form-item label="数据位" v-if="currentCaps.data_bits_options?.length">
            <el-radio-group v-model="form.config.data_bits">
              <el-radio-button v-for="b in currentCaps.data_bits_options" :key="b" :value="b">{{ b }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="停止位" v-if="currentCaps.stop_bits_options?.length">
            <el-radio-group v-model="form.config.stop_bits">
              <el-radio-button v-for="b in currentCaps.stop_bits_options" :key="b" :value="b">{{ b }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="校验" v-if="currentCaps.parity_options?.length">
            <el-select v-model="form.config.parity" style="width: 100%;">
              <el-option v-for="p in currentCaps.parity_options" :key="p" :label="parityLabel(p)" :value="p" />
            </el-select>
          </el-form-item>
          <el-form-item label="流控" v-if="currentCaps.flow_control_options?.length">
            <el-select v-model="form.config.flow_control" style="width: 100%;">
              <el-option v-for="f in currentCaps.flow_control_options" :key="f" :label="flowControlLabel(f)" :value="f" />
            </el-select>
          </el-form-item>
        </template>

        <!-- I2C 参数 -->
        <template v-if="form.hardware_type === 'i2c' && currentCaps">
          <el-form-item label="从机地址" prop="address">
            <el-input v-model="form.address" placeholder="如: 0x40 或 64">
              <template #append>
                <el-button @click="scanI2C" :loading="scanning" :disabled="!form.hardware_id">
                  <el-icon><Search /></el-icon> 扫描
                </el-button>
              </template>
            </el-input>
            <div class="cap-hint" v-if="currentCaps.address_min !== undefined">
              范围: 0x{{ currentCaps.address_min.toString(16).toUpperCase() }} ~ 0x{{ currentCaps.address_max.toString(16).toUpperCase() }}
              <span v-if="currentCaps.supports_10bit"> (支持10位地址)</span>
              <span v-if="currentCaps.supports_scan"> | 支持自动扫描</span>
            </div>
          </el-form-item>
          <el-form-item label="时钟频率 (Hz)">
            <el-input-number
              v-model="form.config.freq_hz"
              :min="currentCaps.freq_hz_min || 10000"
              :max="currentCaps.freq_hz_max || 1000000"
              :step="10000"
              style="width: 100%;"
            />
            <div class="cap-hint" v-if="currentCaps.freq_hz_min">
              范围: {{ currentCaps.freq_hz_min }} ~ {{ currentCaps.freq_hz_max }} Hz
            </div>
          </el-form-item>
        </template>

        <!-- I2C 无能力数据时的地址输入（兜底） -->
        <el-form-item v-if="form.hardware_type === 'i2c' && !currentCaps" label="从机地址" prop="address">
          <el-input v-model="form.address" placeholder="如: 0x40 或 64" />
          <div class="form-tip">支持十六进制 (0x40) 或十进制 (64) 格式</div>
        </el-form-item>

        <!-- SPI 参数 -->
        <template v-if="form.hardware_type === 'spi' && currentCaps">
          <el-form-item label="CS 引脚">
            <el-select v-model="form.config.cs_pin" style="width: 100%;" v-if="currentCaps.cs_pins?.length">
              <el-option v-for="p in currentCaps.cs_pins" :key="p" :label="`CS${p} (GPIO${p})`" :value="p" />
            </el-select>
            <el-input-number v-else v-model="form.config.cs_pin" :min="0" :max="48" style="width: 100%;" />
          </el-form-item>
          <el-form-item label="时钟频率 (Hz)">
            <el-input-number
              v-model="form.config.freq_hz"
              :min="currentCaps.freq_hz_min || 100000"
              :max="currentCaps.freq_hz_max || 40000000"
              :step="100000"
              style="width: 100%;"
            />
            <div class="cap-hint" v-if="currentCaps.freq_hz_min">
              范围: {{ currentCaps.freq_hz_min }} ~ {{ currentCaps.freq_hz_max }} Hz
            </div>
          </el-form-item>
          <el-form-item label="SPI 模式" v-if="currentCaps.mode_options?.length">
            <el-radio-group v-model="form.config.spi_mode">
              <el-radio-button v-for="m in currentCaps.mode_options" :key="m" :value="m">Mode {{ m }}</el-radio-button>
            </el-radio-group>
          </el-form-item>
        </template>

        <!-- SPI 无能力数据时的兜底 -->
        <template v-if="form.hardware_type === 'spi' && !currentCaps">
          <el-form-item label="CS 引脚">
            <el-input-number v-model="form.config.cs_pin" :min="0" :max="48" style="width: 100%;" />
          </el-form-item>
        </template>

        <!-- GPIO 参数 -->
        <template v-if="form.hardware_type === 'gpio' && currentCaps">
          <el-form-item label="方向" v-if="currentCaps.direction_options?.length">
            <el-select v-model="form.config.direction" style="width: 100%;">
              <el-option v-for="d in currentCaps.direction_options" :key="d" :label="directionLabel(d)" :value="d" />
            </el-select>
          </el-form-item>
        </template>

        <!-- ADC 参数 -->
        <template v-if="form.hardware_type === 'adc' && currentCaps">
          <el-form-item label="衰减" v-if="currentCaps.attenuation_options?.length">
            <el-radio-group v-model="form.config.attenuation">
              <el-radio-button v-for="a in currentCaps.attenuation_options" :key="a" :value="a">
                {{ attenuationLabel(a) }}
              </el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="位宽" v-if="currentCaps.bit_width_options?.length">
            <el-radio-group v-model="form.config.bit_width">
              <el-radio-button v-for="b in currentCaps.bit_width_options" :key="b" :value="b">{{ b }}bit</el-radio-button>
            </el-radio-group>
          </el-form-item>
        </template>

        <!-- 通道名称 -->
        <el-form-item label="通道名称">
          <el-input v-model="form.name" placeholder="可选，留空自动生成" />
        </el-form-item>

        <!-- 状态 -->
        <el-form-item label="状态">
          <el-switch v-model="form.enabled" />
          <span style="margin-left: 8px;">{{ form.enabled ? '启用' : '禁用' }}</span>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button v-if="readonly" type="primary" @click="showDialog = false">关闭</el-button>
        <template v-else>
          <el-button @click="showDialog = false">取消</el-button>
          <el-button type="primary" :loading="submitting" @click="handleSubmit">
            {{ editingChannel ? '保存' : '创建' }}
          </el-button>
        </template>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { ElMessage, type FormInstance } from 'element-plus'
import { channelApi, type Channel } from '@/api/channel'
import { nodeApi } from '@/api/node'

interface Props {
  collectorId: number
  modelValue: boolean
  capabilities?: any
  initialData?: any | null
  /** 预选硬件类型（从硬件卡片点击"创建通道"时传入） */
  presetHardwareType?: string
  /** 预选硬件ID */
  presetHardwareId?: string
  collectorStatus?: string
  readonly?: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'refresh': []
}>()

const showDialog = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v)
})

const submitting = ref(false)
const scanning = ref(false)
const editingChannel = computed(() => props.initialData ?? null)
const readonly = computed(() => props.readonly ?? false)
const formRef = ref<FormInstance>()

const form = reactive({
  name: '',
  hardware_type: 'i2c' as string,
  hardware_id: '',
  address: '',
  enabled: true,
  config: {} as Record<string, any>
})

const rules = {
  hardware_type: [{ required: true, message: '请选择硬件类型', trigger: 'change' }],
  hardware_id: [{ required: true, message: '请选择硬件资源', trigger: 'change' }]
}

// ====== 能力数据 ======

// 获取当前选中硬件的能力数据
const currentCaps = computed(() => {
  if (!props.capabilities?.buses || !form.hardware_type || !form.hardware_id) return null
  const hwList = props.capabilities.buses[form.hardware_type]
  if (!hwList) return null
  const hw = hwList.find((h: any) => String(h.id) === form.hardware_id)
  if (!hw) return null

  // 1) 尝试嵌套结构: hw.Capabilities.UartCaps (Go protobuf JSON)
  const capsWrapper = hw.Capabilities || hw.capabilities
  if (capsWrapper) {
    const capsKeyMap: Record<string, string> = {
      uart: 'UartCaps',
      i2c: 'I2CCaps',
      spi: 'SpiCaps',
      gpio: 'GpioCaps',
      adc: 'AdcCaps'
    }
    const capsKey = capsKeyMap[form.hardware_type]
    const nested = capsWrapper[capsKey] || capsWrapper[capsKey?.toLowerCase()]
    if (nested) return nested
  }

  // 2) 无嵌套包装时，直接从 hw 本身提取参数
  switch (form.hardware_type) {
    case 'uart':
      return {
        baud_rate_max: hw.max_baud || 921600,
        baud_rate_min: hw.min_baud || 1200,
        data_bits_options: hw.data_bits_options || [5, 6, 7, 8],
        stop_bits_options: hw.stop_bits_options || [1, 1.5, 2],
        parity_options: hw.parity_options || ['none', 'odd', 'even'],
        flow_control_options: hw.flow_control_options || ['none', 'rts_cts', 'xon_xoff']
      }
    case 'i2c':
      return {
        freq_hz_max: hw.max_freq_hz || 1000000,
        freq_hz_min: hw.min_freq_hz || 10000,
        address_min: hw.address_min ?? 8,
        address_max: hw.address_max ?? 119,
        supports_scan: hw.supports_scan ?? true,
        supports_10bit: hw.supports_10bit || false
      }
    case 'spi':
      return {
        freq_hz_max: hw.max_freq_hz || 40000000,
        freq_hz_min: hw.min_freq_hz || 100000,
        mode_options: hw.mode_options || [0, 1, 2, 3],
        cs_pins: hw.cs_pins || (hw.default_cs_pin != null ? [hw.default_cs_pin] : [])
      }
    case 'gpio':
      return {
        direction_options: hw.direction_options || ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN']
      }
    case 'adc':
      return {
        attenuation_options: hw.attenuation_options || [0, 1, 2, 3],
        bit_width_options: hw.bit_width_options || [9, 10, 11, 12]
      }
    default:
      return null
  }
})

// 获取指定类型的硬件列表（完整对象）
const availableHardwareList = computed(() => {
  if (!props.capabilities?.buses?.[form.hardware_type]) return []
  return props.capabilities.buses[form.hardware_type] || []
})

// ====== 标签/格式化辅助 ======

const parityLabel = (p: string) => ({ none: '无', odd: '奇校验', even: '偶校验', mark: 'Mark', space: 'Space' }[p] || p)
const flowControlLabel = (f: string) => ({ none: '无', rts_cts: 'RTS/CTS', xon_xoff: 'XON/XOFF' }[f] || f)
const directionLabel = (d: string) => ({ INPUT: '输入', OUTPUT: '输出', INPUT_PULLUP: '输入上拉', INPUT_PULLDOWN: '输入下拉' }[d] || d)
const attenuationLabel = (a: number) => ({ 0: '0dB', 1: '2.5dB', 2: '6dB', 3: '11dB' }[a] || `${a}`)

const formatPinsBrief = (pins: any[]) => {
  if (!pins?.length) return ''
  return pins
    .filter((p: any) => p.role && p.role !== 0)
    .map((p: any) => {
      const labels: Record<number, string> = { 1:'TX',2:'RX',3:'SDA',4:'SCL',5:'MOSI',6:'MISO',7:'SCLK',8:'CS',9:'GPIO',10:'CH' }
      return `${labels[p.role]||p.role}=${p.pin}`
    })
    .join(' ')
}

const computeBaudStep = () => {
  if (!currentCaps.value) return 1200
  const { baud_rate_min = 1200, baud_rate_max = 921600 } = currentCaps.value
  const range = baud_rate_max - baud_rate_min
  if (range > 1000000) return 115200
  if (range > 100000) return 9600
  return 1200
}

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
    form.config.baud_rate = 115200
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
  } else if (form.hardware_type === 'gpio') {
    form.config.direction = caps.direction_options?.includes('INPUT') ? 'INPUT' : (caps.direction_options?.[0] || 'INPUT')
  } else if (form.hardware_type === 'adc') {
    form.config.attenuation = caps.attenuation_options?.includes(3) ? 3 : (caps.attenuation_options?.[0] ?? 3)
    form.config.bit_width = caps.bit_width_options?.includes(12) ? 12 : (caps.bit_width_options?.[0] ?? 12)
  }
}

// I2C 扫描
const scanI2C = async () => {
  if (!form.hardware_id) return
  scanning.value = true
  try {
    const result = await nodeApi.scanI2C(props.collectorId, form.hardware_id)
    if (result?.devices?.length) {
      ElMessage.success(`发现 ${result.devices.length} 个设备: ${result.devices.join(', ')}`)
      // 自动填入第一个地址
      if (!form.address && result.devices[0]) {
        form.address = result.devices[0]
      }
    } else {
      ElMessage.info('未发现 I2C 设备')
    }
  } catch (e: any) {
    ElMessage.warning('扫描失败: ' + (e.message || '未知错误'))
  } finally {
    scanning.value = false
  }
}

const handleSubmit = async () => {
  if (!formRef.value) return
  if (readonly.value) return
  try {
    await formRef.value.validate()
    submitting.value = true

    // 处理地址
    let address = form.address
    if (form.hardware_type === 'spi') {
      address = String(form.config.cs_pin ?? 0)
    }

    // 组装 config
    const config: Record<string, any> = { ...form.config }

    // 组装 bus_config (硬件总线配置的hex字节数组)
    let busConfig = ''
    const hw = availableHardwareList.value.find(h => String(h.id) === form.hardware_id)
    if (form.hardware_type === 'uart' && hw) {
      const tx_pin = form.config.tx_pin || hw.default_tx_pin
      const rx_pin = form.config.rx_pin || hw.default_rx_pin
      const baud = form.config.baud_rate || 115200
      // bus_config: [tx_pin(1B)][rx_pin(1B)][baud(4B BE)]
      const buf = new Uint8Array(6)
      buf[0] = tx_pin
      buf[1] = rx_pin
      buf[2] = (baud >> 24) & 0xFF
      buf[3] = (baud >> 16) & 0xFF
      buf[4] = (baud >> 8) & 0xFF
      buf[5] = baud & 0xFF
      busConfig = Array.from(buf).map(b => b.toString(16).padStart(2,'0')).join('').toUpperCase()
    } else if (form.hardware_type === 'i2c' && hw) {
      const sda = form.config.sda_pin || hw.default_sda_pin
      const scl = form.config.scl_pin || hw.default_scl_pin
      const addr = parseInt(form.address, 16) || 0
      const freq = form.config.freq_hz || hw.max_freq_hz || 400000
      const buf = new Uint8Array(7)
      buf[0] = sda; buf[1] = scl; buf[2] = addr & 0xFF
      buf[3] = (freq >> 24) & 0xFF; buf[4] = (freq >> 16) & 0xFF
      buf[5] = (freq >> 8) & 0xFF; buf[6] = freq & 0xFF
      busConfig = Array.from(buf).map(b => b.toString(16).padStart(2,'0')).join('').toUpperCase()
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
      busConfig = Array.from(buf).map(b => b.toString(16).padStart(2,'0')).join('').toUpperCase()
    }

    const data: Partial<Channel> = {
      node_id: String(props.collectorId),
      hardware_type: form.hardware_type,
      hardware_id: form.hardware_id,
      bus_type: form.hardware_type.toUpperCase(),
      address: address || undefined,
      name: form.name || undefined,
      enabled: form.enabled,
      bus_config: busConfig || undefined,
      config: JSON.stringify(config)
    }

    if (editingChannel.value?.id) {
      await channelApi.update(editingChannel.value.id, data)
      // 自动同步配置到采集器
      try {
        await nodeApi.syncConfig(props.collectorId)
        ElMessage.success('更新成功，配置已同步到采集器')
      } catch (syncError: any) {
        ElMessage.warning('更新成功，但配置同步失败：' + (syncError.message || '采集器可能离线'))
      }
    } else {
      await channelApi.create(data as any)
      // 自动同步配置到采集器
      try {
        await nodeApi.syncConfig(props.collectorId)
        ElMessage.success('创建成功，配置已同步到采集器')
      } catch (syncError: any) {
        ElMessage.warning('创建成功，但配置同步失败：' + (syncError.message || '采集器可能离线'))
      }
    }

    showDialog.value = false
    emit('refresh')
  } catch (error: any) {
    if (error !== 'cancel') ElMessage.error(error.message || '操作失败')
  } finally {
    submitting.value = false
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

// ====== 初始化：预选硬件 ======

watch(showDialog, (open) => {
  if (!open) return

  if (editingChannel.value) {
    // 编辑模式：从通道数据填充
    form.hardware_type = editingChannel.value.hardware_type || 'i2c'
    form.hardware_id = editingChannel.value.hardware_id || ''
    form.address = editingChannel.value.address || ''
    form.name = editingChannel.value.name || ''
    form.enabled = editingChannel.value.status === 'active'
    form.config = { ...(editingChannel.value.config || {}) }
    if (form.hardware_type === 'spi' && editingChannel.value.address) {
      form.config.cs_pin = parseInt(editingChannel.value.address)
    }
  } else if (props.presetHardwareType) {
    // 预选模式：从硬件卡片传入
    form.hardware_type = props.presetHardwareType
    form.hardware_id = props.presetHardwareId || ''
    form.name = ''
    form.address = ''
    form.enabled = true
    form.config = {}
    // 延迟初始化默认值（等 currentCaps 计算完成）
    setTimeout(() => initConfigDefaults(), 50)
  } else {
    resetForm()
  }
})
</script>

<style scoped>
.channel-manager {
  padding: 0;
}

.form-tip {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}

.cap-hint {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
  line-height: 1.5;
}

.cap-hint span {
  color: #67c23a;
}

/* 预选硬件时只读字段的视觉提示 */
:deep(.el-select.is-disabled .el-input__inner) {
  color: #303133;
  -webkit-text-fill-color: #303133;
}
</style>

<!-- Global styles for the el-dialog custom-class (scoped CSS cannot reach dialog overlay) -->
<style>
.channel-manager-dialog {
  display: flex;
  flex-direction: column;
  max-height: 80vh;
}

.channel-manager-dialog .el-dialog__body {
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}

.channel-manager-dialog .el-dialog__footer {
  flex-shrink: 0;
  border-top: 1px solid #ebeef5;
  padding: 12px 20px;
}
</style>
