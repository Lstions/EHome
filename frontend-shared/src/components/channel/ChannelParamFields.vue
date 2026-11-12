<template>
  <!-- ====== 能力驱动的配置参数 ====== -->
  <!-- 边界依据：这一整块只由 form.hardware_type 与 caps（当前资源能力）驱动，
       不碰 store / 事务代际 / 调用方 props 契约，因此可独立成子组件。
       它仍渲染在父级 el-form 的插槽里，校验与 disabled 上下文原样继承。
       I2C 扫描是带请求编排的副作用，通过 scan 事件交回父组件执行。 -->

  <!-- UART 参数 -->
  <template v-if="form.hardware_type === 'uart' && caps">
    <el-form-item label="波特率">
      <el-input-number
        v-model="form.config.baud_rate"
        :min="caps.baud_rate_min || 1200"
        :max="caps.baud_rate_max || 921600"
        :step="computeBaudStep()"
        style="width: 100%;"
      />
      <div class="cap-hint" v-if="caps.baud_rate_min">
        范围: {{ caps.baud_rate_min }} ~ {{ caps.baud_rate_max }}
      </div>
    </el-form-item>
    <el-form-item label="数据位" v-if="caps.data_bits_options?.length">
      <el-radio-group v-model="form.config.data_bits">
        <el-radio-button v-for="b in caps.data_bits_options" :key="b" :value="b">{{ b }}</el-radio-button>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="停止位" v-if="caps.stop_bits_options?.length">
      <el-radio-group v-model="form.config.stop_bits">
        <el-radio-button v-for="b in caps.stop_bits_options" :key="b" :value="b">{{ b }}</el-radio-button>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="校验" v-if="caps.parity_options?.length">
      <el-select v-model="form.config.parity" style="width: 100%;">
        <el-option v-for="p in caps.parity_options" :key="p" :label="parityLabel(p)" :value="p" />
      </el-select>
    </el-form-item>
    <el-form-item label="流控" v-if="caps.flow_control_options?.length">
      <el-select v-model="form.config.flow_control" style="width: 100%;">
        <el-option v-for="f in caps.flow_control_options" :key="f" :label="flowControlLabel(f)" :value="f" />
      </el-select>
    </el-form-item>
  </template>

  <!-- UART 无能力数据时的保守编辑兜底：保持已有配置可修改，
       不将能力暂缺误判为不可编辑。资源约束仍由后端 manifest 校验。 -->
  <template v-if="form.hardware_type === 'uart' && !caps">
    <el-form-item label="波特率">
      <el-input-number v-model="form.config.baud_rate" :min="1200" :max="921600" :step="1200" style="width: 100%;" />
    </el-form-item>
    <el-form-item label="数据位">
      <el-radio-group v-model="form.config.data_bits">
        <el-radio-button v-for="bit in [5, 6, 7, 8]" :key="bit" :value="bit">{{ bit }}</el-radio-button>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="停止位">
      <el-radio-group v-model="form.config.stop_bits">
        <el-radio-button v-for="bit in [1, 2]" :key="bit" :value="bit">{{ bit }}</el-radio-button>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="校验">
      <el-select v-model="form.config.parity" style="width: 100%;">
        <el-option label="无" value="none" />
        <el-option label="奇校验" value="odd" />
        <el-option label="偶校验" value="even" />
      </el-select>
    </el-form-item>
  </template>

  <!-- I2C 参数 -->
  <template v-if="form.hardware_type === 'i2c' && caps">
    <el-form-item label="从机地址" prop="address">
      <el-input v-model="form.address" placeholder="如: 0x40 或 64">
        <template #append>
          <el-button @click="emit('scan')" :loading="scanning" :disabled="!form.hardware_id">
            <el-icon><Search /></el-icon> 扫描
          </el-button>
        </template>
      </el-input>
      <div class="cap-hint" v-if="caps.address_min !== undefined">
        范围: 0x{{ caps.address_min.toString(16).toUpperCase() }} ~ 0x{{ caps.address_max.toString(16).toUpperCase() }}
        <span v-if="caps.supports_10bit"> (支持10位地址)</span>
        <span v-if="caps.supports_scan"> | 支持自动扫描</span>
      </div>
    </el-form-item>
    <el-form-item label="时钟频率 (Hz)">
      <el-input-number
        v-model="form.config.freq_hz"
        :min="caps.freq_hz_min || 10000"
        :max="caps.freq_hz_max || 1000000"
        :step="10000"
        style="width: 100%;"
      />
      <div class="cap-hint" v-if="caps.freq_hz_min">
        范围: {{ caps.freq_hz_min }} ~ {{ caps.freq_hz_max }} Hz
      </div>
    </el-form-item>
  </template>

  <!-- I2C 无能力数据时的地址输入（兜底） -->
  <el-form-item v-if="form.hardware_type === 'i2c' && !caps" label="从机地址" prop="address">
    <el-input v-model="form.address" placeholder="如: 0x40 或 64" />
    <div class="form-tip">支持十六进制 (0x40) 或十进制 (64) 格式</div>
  </el-form-item>

  <!-- SPI 参数 -->
  <template v-if="form.hardware_type === 'spi' && caps">
    <el-form-item label="CS 引脚">
      <el-select v-model="form.config.cs_pin" style="width: 100%;" v-if="caps.cs_pins?.length">
        <el-option v-for="p in caps.cs_pins" :key="p" :label="`CS${p} (GPIO${p})`" :value="p" />
      </el-select>
      <el-input-number v-else v-model="form.config.cs_pin" :min="0" :max="48" style="width: 100%;" />
    </el-form-item>
    <el-form-item label="时钟频率 (Hz)">
      <el-input-number
        v-model="form.config.freq_hz"
        :min="caps.freq_hz_min || 100000"
        :max="caps.freq_hz_max || 40000000"
        :step="100000"
        style="width: 100%;"
      />
      <div class="cap-hint" v-if="caps.freq_hz_min">
        范围: {{ caps.freq_hz_min }} ~ {{ caps.freq_hz_max }} Hz
      </div>
    </el-form-item>
    <el-form-item label="SPI 模式" v-if="caps.mode_options?.length">
      <el-radio-group v-model="form.config.spi_mode">
        <el-radio-button v-for="m in caps.mode_options" :key="m" :value="m">Mode {{ m }}</el-radio-button>
      </el-radio-group>
    </el-form-item>
  </template>

  <!-- SPI 无能力数据时的兜底 -->
  <template v-if="form.hardware_type === 'spi' && !caps">
    <el-form-item label="CS 引脚">
      <el-input-number v-model="form.config.cs_pin" :min="0" :max="48" style="width: 100%;" />
    </el-form-item>
  </template>

  <!-- ADC 参数 -->
  <template v-if="form.hardware_type === 'adc' && caps">
    <el-form-item label="衰减" v-if="caps.attenuation_options?.length">
      <el-radio-group v-model="form.config.attenuation">
        <el-radio-button v-for="a in caps.attenuation_options" :key="a" :value="a">
          {{ attenuationLabel(a) }}
        </el-radio-button>
      </el-radio-group>
    </el-form-item>
    <el-form-item label="位宽" v-if="caps.bit_width_options?.length">
      <el-radio-group v-model="form.config.bit_width">
        <el-radio-button v-for="b in caps.bit_width_options" :key="b" :value="b">{{ b }}bit</el-radio-button>
      </el-radio-group>
    </el-form-item>
  </template>
</template>

<script setup lang="ts">
import { Search } from '@element-plus/icons-vue'
import type { ChannelFormState } from './channelFormModel'

/**
 * 通道「能力驱动参数」字段组。
 *
 * 为什么是子组件而不是继续内联：UART/I2C/SPI/ADC 四套参数只依赖
 * form.hardware_type + caps，是模板里唯一可整体切走的自包含块；
 * 字段顺序、label、cap-hint 文案、class 原样保留，父级 el-form 的
 * rules / disabled 通过组件树上下文继续生效。
 *
 * 为什么扫描用事件回抛：scanI2C 需要 props.modelValue / collectorId 的
 * 事务校验与 nodeApi 请求编排，属于父组件的调用方契约，不随纯表单字段下沉。
 */
const props = defineProps<{
  form: ChannelFormState
  caps: any
  scanning: boolean
}>()

const emit = defineEmits<{ scan: [] }>()

// ====== 标签/格式化辅助（仅本字段组使用） ======

const parityLabel = (p: string) => ({ none: '无', odd: '奇校验', even: '偶校验', mark: 'Mark', space: 'Space' }[p] || p)
const flowControlLabel = (f: string) => ({ none: '无', rts_cts: 'RTS/CTS', xon_xoff: 'XON/XOFF' }[f] || f)
const attenuationLabel = (a: number) => ({ 0: '0dB', 1: '2.5dB', 2: '6dB', 3: '11dB' }[a] || `${a}`)

const computeBaudStep = () => {
  if (!props.caps) return 1200
  const { baud_rate_min = 1200, baud_rate_max = 921600 } = props.caps
  const range = baud_rate_max - baud_rate_min
  if (range > 1000000) return 115200
  if (range > 100000) return 9600
  return 1200
}
</script>

<style scoped>
.form-tip {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

.cap-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
  line-height: 1.5;
}

.cap-hint span {
  color: var(--el-color-success);
}
</style>
