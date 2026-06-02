<template>
  <el-dialog
    v-model="dialogVisible"
    title="配置外设设备"
    width="600px"
    :close-on-click-modal="false"
  >
    <el-form :model="form" :rules="rules" label-width="100px" ref="formRef">
      <el-form-item label="外设">
        <el-input :value="peripheralLabel" disabled />
      </el-form-item>

      <el-form-item label="设备类型" prop="device_type">
        <el-select
          v-model="form.device_type"
          placeholder="请选择设备类型"
          style="width: 100%;"
          @change="handleDeviceTypeChange"
        >
          <el-option
            v-for="item in deviceTypeOptions"
            :key="item.value"
            :label="item.label"
            :value="item.value"
          />
        </el-select>
      </el-form-item>

      <el-form-item label="设备名称" prop="device_name">
        <el-input
          v-model="form.device_name"
          placeholder="请输入设备名称"
          :maxlength="50"
          show-word-limit
        />
      </el-form-item>

      <!-- 配置模板选择 -->
      <el-form-item label="配置模板">
        <el-select
          v-model="form.template_id"
          placeholder="选择配置模板（可选）"
          style="width: 100%;"
          clearable
          :loading="templatesLoading"
          @change="handleTemplateChange"
        >
          <el-option
            v-for="item in templates"
            :key="item.id"
            :label="item.name + (item.is_default ? ' (默认)' : '')"
            :value="item.id"
          >
            <div class="template-option">
              <span>{{ item.name }}</span>
              <el-tag v-if="item.is_default" type="success" size="small">默认</el-tag>
            </div>
          </el-option>
        </el-select>
        <div v-if="templates.length === 0 && form.device_type" class="no-template-hint">
          暂无该设备类型的配置模板，请手动配置
        </div>
      </el-form-item>

      <el-form-item label="通信协议" prop="protocol">
        <el-select
          v-model="form.protocol"
          placeholder="请选择通信协议"
          style="width: 100%;"
        >
          <el-option label="MODBUS RTU" value="modbus" />
          <el-option label="TTL 字节流" value="stream" />
        </el-select>
      </el-form-item>

      <!-- MODBUS 配置 -->
      <template v-if="form.protocol === 'modbus'">
        <el-divider content-position="left">MODBUS 配置</el-divider>

        <el-form-item label="从机地址" prop="config.modbus_address">
          <el-input-number
            v-model.number="form.config.modbus_address"
            :min="1"
            :max="247"
            placeholder="1-247"
            style="width: 100%;"
          />
        </el-form-item>

        <el-form-item label="波特率">
          <el-select v-model="form.config.baudrate" style="width: 100%;">
            <el-option :value="1200" label="1200" />
            <el-option :value="2400" label="2400" />
            <el-option :value="4800" label="4800" />
            <el-option :value="9600" label="9600" />
            <el-option :value="19200" label="19200" />
            <el-option :value="38400" label="38400" />
            <el-option :value="57600" label="57600" />
            <el-option :value="115200" label="115200" />
          </el-select>
        </el-form-item>

        <el-form-item label="数据位">
          <el-select v-model="form.config.data_bits" style="width: 100%;">
            <el-option :value="7" label="7" />
            <el-option :value="8" label="8" />
          </el-select>
        </el-form-item>

        <el-form-item label="停止位">
          <el-select v-model="form.config.stop_bits" style="width: 100%;">
            <el-option :value="1" label="1" />
            <el-option :value="2" label="2" />
          </el-select>
        </el-form-item>

        <el-form-item label="校验位">
          <el-select v-model="form.config.parity" style="width: 100%;">
            <el-option value="none" label="无校验" />
            <el-option value="even" label="偶校验" />
            <el-option value="odd" label="奇校验" />
          </el-select>
        </el-form-item>
      </template>

      <!-- Stream 配置 -->
      <template v-if="form.protocol === 'stream'">
        <el-divider content-position="left">字节流配置</el-divider>

        <el-form-item label="波特率">
          <el-select v-model="form.config.baudrate" style="width: 100%;">
            <el-option :value="1200" label="1200" />
            <el-option :value="2400" label="2400" />
            <el-option :value="4800" label="4800" />
            <el-option :value="9600" label="9600" />
            <el-option :value="19200" label="19200" />
            <el-option :value="38400" label="38400" />
            <el-option :value="57600" label="57600" />
            <el-option :value="115200" label="115200" />
          </el-select>
        </el-form-item>

        <el-form-item label="帧分隔符">
          <el-input
            v-model="form.config.frame_delimiter"
            placeholder="如: \r\n"
          />
        </el-form-item>
      </template>
    </el-form>

    <template #footer>
      <el-button @click="dialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">
        确定分配
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { collectorApi, type PeripheralAssignment } from '@/api/collector'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'

const props = withDefaults(defineProps<{
  visible: boolean
  collectorId: number
  peripheralType: 'uart' | 'i2c' | 'spi'
  peripheralId: string
  peripheralMode?: string
}>(), {
  visible: false,
  collectorId: 0,
  peripheralType: 'uart',
  peripheralId: '',
  peripheralMode: ''
})

const emit = defineEmits<{
  (e: 'success'): void
  (e: 'update:visible', val: boolean): void
}>()

const formRef = ref()
const submitting = ref(false)
const templates = ref<DeviceConfig[]>([])
const templatesLoading = ref(false)

const dialogVisible = computed({
  get: () => props.visible,
  set: (val) => emit('update:visible', val)
})

const peripheralLabel = computed(() => {
  const typeNames: Record<string, string> = {
    uart: 'UART',
    i2c: 'I2C',
    spi: 'SPI'
  }
  let label = `${typeNames[props.peripheralType] || props.peripheralType} - ${props.peripheralId}`
  if (props.peripheralMode) {
    label += ` (${props.peripheralMode})`
  }
  return label
})

interface FormConfig {
  modbus_address: number
  baudrate: number
  data_bits: number
  stop_bits: number
  parity: string
  frame_delimiter: string
}

interface FormState {
  device_type: string
  device_name: string
  protocol: 'modbus' | 'stream'
  template_id: number | undefined
  config: FormConfig
}

const form = reactive<FormState>({
  device_type: '',
  device_name: '',
  protocol: 'modbus',
  template_id: undefined,
  config: {
    modbus_address: 1,
    baudrate: 9600,
    data_bits: 8,
    stop_bits: 1,
    parity: 'none',
    frame_delimiter: '\r\n'
  }
})

const rules = {
  device_type: [{ required: true, message: '请选择设备类型', trigger: 'change' }],
  device_name: [{ required: true, message: '请输入设备名称', trigger: 'blur' }],
  protocol: [{ required: true, message: '请选择通信协议', trigger: 'change' }],
  'config.modbus_address': [{ required: true, message: '请输入从机地址', trigger: 'blur' }]
}

const deviceTypeOptions = [
  { value: 'wind_speed', label: '风速传感器' },
  { value: 'wind_direction', label: '风向传感器' },
  { value: 'rain', label: '雨量传感器' },
  { value: 'light', label: '光照传感器' },
  { value: 'temp_humidity', label: '温湿度传感器' },
  { value: 'battery', label: '电池保护板' },
  { value: 'inverter', label: '光伏逆变器' }
]

const deviceTypeNames: Record<string, string> = {
  wind_speed: '风速传感器',
  wind_direction: '风向传感器',
  rain: '雨量传感器',
  light: '光照传感器',
  temp_humidity: '温湿度传感器',
  battery: '电池保护板',
  inverter: '光伏逆变器'
}

// 加载设备类型的配置模板
const loadTemplates = async (deviceType: string) => {
  if (!deviceType) {
    templates.value = []
    return
  }

  templatesLoading.value = true
  try {
    templates.value = await deviceConfigApi.getByDeviceType(deviceType)
  } catch {
    templates.value = []
  } finally {
    templatesLoading.value = false
  }
}

// 设备类型变更
const handleDeviceTypeChange = async (value: string) => {
  // 清除模板选择
  form.template_id = undefined

  // 自动填充设备名称
  if (value && !form.device_name) {
    form.device_name = deviceTypeNames[value] || value
  }

  // 根据设备类型自动选择协议
  if (['battery', 'light', 'rain'].includes(value)) {
    form.protocol = 'stream'
  } else {
    form.protocol = 'modbus'
  }

  // 加载配置模板
  await loadTemplates(value)

  // 如果有默认模板，自动选择并填充配置
  const defaultTemplate = templates.value.find(t => t.is_default)
  if (defaultTemplate) {
    form.template_id = defaultTemplate.id
    applyTemplateConfig(defaultTemplate)
  }
}

// 模板选择变更
const handleTemplateChange = (templateId: number | undefined) => {
  if (!templateId) {
    // 清除模板选择时重置为默认配置
    resetConfig()
    return
  }

  const template = templates.value.find(t => t.id === templateId)
  if (template) {
    applyTemplateConfig(template)
  }
}

// 应用模板配置
const applyTemplateConfig = (template: DeviceConfig) => {
  const config = template.config || {}

  // 设置协议
  if (template.protocol) {
    form.protocol = template.protocol
  }

  // 填充配置参数
  if (config.modbus_address !== undefined) {
    form.config.modbus_address = config.modbus_address
  }
  if (config.baudrate !== undefined) {
    form.config.baudrate = config.baudrate
  }
  if (config.data_bits !== undefined) {
    form.config.data_bits = config.data_bits
  }
  if (config.stop_bits !== undefined) {
    form.config.stop_bits = config.stop_bits
  }
  if (config.parity !== undefined) {
    form.config.parity = config.parity
  }
  if (config.frame_delimiter !== undefined) {
    form.config.frame_delimiter = config.frame_delimiter
  }
}

// 重置配置为默认值
const resetConfig = () => {
  form.config = {
    modbus_address: 1,
    baudrate: 9600,
    data_bits: 8,
    stop_bits: 1,
    parity: 'none',
    frame_delimiter: '\r\n'
  }
}

const handleSubmit = async () => {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  submitting.value = true
  try {
    // 构建配置对象
    let configData: Record<string, any> | undefined
    if (form.protocol === 'modbus') {
      configData = {
        modbus_address: form.config.modbus_address,
        baudrate: form.config.baudrate,
        data_bits: form.config.data_bits,
        stop_bits: form.config.stop_bits,
        parity: form.config.parity
      }
    } else if (form.protocol === 'stream') {
      configData = {
        baudrate: form.config.baudrate,
        frame_delimiter: form.config.frame_delimiter
      }
    }

    const assignment: PeripheralAssignment = {
      peripheral_type: props.peripheralType,
      peripheral_id: props.peripheralId,
      device_type: form.device_type,
      device_name: form.device_name,
      protocol: form.protocol,
      template_id: form.template_id,
      config: configData
    }

    await collectorApi.assignPeripheral(props.collectorId, assignment)
    ElMessage.success('外设分配成功')
    emit('success')
    dialogVisible.value = false
  } catch (error: any) {
    ElMessage.error(error.message || '外设分配失败')
  } finally {
    submitting.value = false
  }
}

// 重置表单
watch(() => props.visible, (newVal) => {
  if (newVal) {
    form.device_type = ''
    form.device_name = ''
    form.protocol = 'modbus'
    form.template_id = undefined
    form.config = {
      modbus_address: 1,
      baudrate: 9600,
      data_bits: 8,
      stop_bits: 1,
      parity: 'none',
      frame_delimiter: '\r\n'
    }
    templates.value = []
    formRef.value?.resetFields()
  }
})
</script>

<style scoped>
.template-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
}

.no-template-hint {
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
}
</style>
