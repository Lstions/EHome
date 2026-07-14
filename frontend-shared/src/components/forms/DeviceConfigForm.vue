<template>
  <el-dialog
    v-model="dialogVisible"
    :title="isEdit ? '编辑配置模板' : '新建配置模板'"
    width="680px"
    :close-on-click-modal="false"
    class="config-form-dialog"
    destroy-on-close
  >
    <el-form :model="form" :rules="rules" label-position="top" ref="formRef">
      <!-- 第一行：模板名称 + 设为默认 -->
      <div class="form-row">
        <el-form-item label="模板名称" prop="name" class="flex-1">
          <el-input
            v-model="form.name"
            placeholder="如: BMP280-I2C-默认配置"
            :maxlength="100"
          />
        </el-form-item>
        <el-form-item label="默认" class="switch-item">
          <el-switch v-model="form.is_default" />
        </el-form-item>
      </div>

      <!-- 驱动选择 -->
      <el-form-item label="传感器驱动" prop="driverPath">
        <el-cascader
          v-model="form.driverPath"
          :options="driverOptions"
          :props="{
            value: 'value',
            label: 'label',
            children: 'children',
            expandTrigger: 'hover'
          }"
          placeholder="选择：OEM → 种类 → 型号"
          style="width: 100%;"
          clearable
          filterable
          @change="onDriverChange"
        >
          <template #default="{ node, data }">
            <div class="driver-option">
              <span>{{ data.label }}</span>
              <el-tag v-if="data.hardware_types?.length" size="small" type="info">{{ data.hardware_types.join(',') }}</el-tag>
            </div>
          </template>
        </el-cascader>
      </el-form-item>

      <!-- 选中的驱动信息 -->
      <el-alert v-if="selectedDriver" :closable="false" type="info" style="margin-bottom: 16px;">
        已选择: <strong>{{ selectedDriver.manufacturer }}</strong> - {{ selectedDriver.model }}
      </el-alert>

      <!-- 第二行：硬件类型 -->
      <div class="form-row">
        <el-form-item label="硬件类型" prop="hardware_type" class="flex-1">
          <el-select v-model="form.hardware_type" placeholder="选择硬件" style="width: 100%;" @change="onHardwareTypeChange">
            <el-option v-for="bus in availableBusTypes" :key="bus.value" :label="bus.label" :value="bus.value" />
          </el-select>
        </el-form-item>
      </div>

      <!-- 通讯协议 (UART时显示) -->
      <div class="form-row" v-if="form.hardware_type === 'uart'">
        <el-form-item label="通讯协议" class="flex-1">
          <el-radio-group v-model="form.protocol">
            <el-radio value="modbus">MODBUS RTU</el-radio>
            <el-radio value="stream">字节流</el-radio>
            <el-radio value="custom">自定义</el-radio>
          </el-radio-group>
        </el-form-item>
      </div>

      <!-- 硬件参数配置 -->
      <el-divider content-position="left">硬件参数</el-divider>

      <!-- UART 参数 -->
      <template v-if="form.hardware_type === 'uart'">
        <div class="params-grid">
          <el-form-item label="波特率">
            <el-select v-model="form.config.baudrate" style="width: 100%;">
              <el-option v-for="b in [9600,19200,38400,57600,115200,230400]" :key="b" :label="b.toString()" :value="b" />
            </el-select>
          </el-form-item>
          <el-form-item label="数据位">
            <el-select v-model="form.config.data_bits" style="width: 100%;">
              <el-option :value="8" label="8 位" />
              <el-option :value="7" label="7 位" />
            </el-select>
          </el-form-item>
          <el-form-item label="停止位">
            <el-select v-model="form.config.stop_bits" style="width: 100%;">
              <el-option :value="1" label="1 位" />
              <el-option :value="2" label="2 位" />
            </el-select>
          </el-form-item>
          <el-form-item label="校验位">
            <el-select v-model="form.config.parity" style="width: 100%;">
              <el-option value="none" label="无" />
              <el-option value="even" label="偶校验" />
              <el-option value="odd" label="奇校验" />
            </el-select>
          </el-form-item>
          <el-form-item label="从机地址" v-if="form.protocol === 'modbus'">
            <el-input-number v-model="form.config.modbus_address" :min="1" :max="247" style="width: 100%;" />
          </el-form-item>
          <el-form-item label="超时(ms)">
            <el-input-number v-model="form.config.timeout_ms" :min="100" :max="10000" :step="100" style="width: 100%;" />
          </el-form-item>
        </div>
      </template>

      <!-- I2C 参数 -->
      <template v-if="form.hardware_type === 'i2c'">
        <div class="params-grid">
          <el-form-item label="角色">
            <el-radio-group v-model="form.config.sensor_role">
              <el-radio value="slave">从机</el-radio>
              <el-radio value="master">主机</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="时钟频率">
            <el-select v-model="form.config.clock_hz" style="width: 100%;">
              <el-option :value="100000" label="100 kHz" />
              <el-option :value="400000" label="400 kHz" />
              <el-option :value="1000000" label="1 MHz" />
            </el-select>
          </el-form-item>
          <el-form-item label="设备地址">
            <el-input v-model="form.config.device_addr" placeholder="如: 0x44" />
          </el-form-item>
          <el-form-item label="寄存器地址">
            <el-input v-model="form.config.register_addr" placeholder="如: 0x00" />
          </el-form-item>
          <el-form-item label="读取长度">
            <el-input-number v-model="form.config.read_length" :min="1" :max="256" style="width: 100%;" />
          </el-form-item>
        </div>
      </template>

      <!-- SPI 参数 -->
      <template v-if="form.hardware_type === 'spi'">
        <div class="params-grid">
          <el-form-item label="角色">
            <el-radio-group v-model="form.config.sensor_role">
              <el-radio value="slave">从机</el-radio>
              <el-radio value="master">主机</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="时钟频率">
            <el-input-number v-model="form.config.clock_hz" :min="1000" :max="80000000" :step="1000000" style="width: 100%;" />
          </el-form-item>
          <el-form-item label="SPI 模式">
            <el-select v-model="form.config.spi_mode" style="width: 100%;">
              <el-option :value="0" label="Mode 0" />
              <el-option :value="1" label="Mode 1" />
              <el-option :value="2" label="Mode 2" />
              <el-option :value="3" label="Mode 3" />
            </el-select>
          </el-form-item>
          <el-form-item label="CS 引脚">
            <el-input-number v-model="form.config.cs_pin" :min="0" :max="48" style="width: 100%;" />
          </el-form-item>
        </div>
      </template>

      <!-- ADC 参数 -->
      <template v-if="form.hardware_type === 'adc'">
        <div class="params-grid">
          <el-form-item label="通道">
            <el-input-number v-model="form.config.channel" :min="0" :max="10" style="width: 100%;" />
          </el-form-item>
          <el-form-item label="衰减">
            <el-select v-model="form.config.attenuation" style="width: 100%;">
              <el-option value="0db" label="0 dB (0-1.1V)" />
              <el-option value="2.5db" label="2.5 dB (0-1.5V)" />
              <el-option value="6db" label="6 dB (0-2.2V)" />
              <el-option value="11db" label="11 dB (0-3.3V)" />
            </el-select>
          </el-form-item>
          <el-form-item label="采样间隔">
            <el-input-number v-model="form.config.interval_ms" :min="0" :max="60000" style="width: 100%;" />
          </el-form-item>
        </div>
      </template>

      <!-- 描述 -->
      <el-form-item label="描述">
        <el-input v-model="form.description" type="textarea" :rows="2" placeholder="可选描述" />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="dialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">
        {{ isEdit ? '保存' : '创建' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import driverApi, { type DriverLeaf } from '@/api/driver'

const props = defineProps<{
  visible: boolean
  config?: DeviceConfig | null
}>()

const emit = defineEmits<{
  (e: 'success'): void
  (e: 'update:visible', val: boolean): void
}>()

const formRef = ref()
const submitting = ref(false)

// 驱动相关
const driverOptions = ref<any[]>([])
const driverList = ref<DriverLeaf[]>([])
const availableBusTypes = ref<{ value: string; label: string }[]>([])

const busOptions = [
  { value: 'uart', label: 'UART' },
  { value: 'i2c', label: 'I2C' },
  { value: 'spi', label: 'SPI' },
  { value: 'adc', label: 'ADC' },
]

const dialogVisible = computed({
  get: () => props.visible,
  set: (val) => emit('update:visible', val)
})

const isEdit = computed(() => !!props.config?.id)

// 选中的驱动
const selectedDriver = computed(() => {
  if (form.driverPath && form.driverPath.length >= 3) {
    return driverList.value.find(d => d.type === form.driverPath[2])
  }
  return null
})

const form = reactive({
  name: '',
  description: '',
  device_type: '',
  driverPath: [] as string[],
  hardware_type: 'uart',
  protocol: 'modbus',
  is_default: false,
  config: {
    baudrate: 9600,
    data_bits: 8,
    stop_bits: 1,
    parity: 'none',
    modbus_address: 1,
    timeout_ms: 1000,
    sensor_role: 'slave',
    clock_hz: 400000,
    device_addr: '',
    register_addr: '',
    read_length: 1,
    interval_ms: 1000,
    spi_mode: 0,
    cs_pin: 0,
    pin: 0,
    direction: 'input',
    pull: 'none',
    channel: 0,
    attenuation: '11db'
  }
})

const rules = {
  name: [{ required: true, message: '请输入模板名称', trigger: 'blur' }],
  driverPath: [{ required: true, message: '请选择传感器驱动', trigger: 'change' }],
  hardware_type: [{ required: true, message: '请选择硬件类型', trigger: 'change' }]
}

// 加载驱动列表
const loadDrivers = async () => {
  try {
    const tree = await driverApi.getDriverTree()
    driverOptions.value = driverApi.transformToCascaderOptions(tree)
    driverList.value = driverApi.flattenDrivers(tree)
  } catch (error) {
    console.error('加载驱动列表失败:', error)
  }
}

// 驱动选择变化
const onDriverChange = (path: string[]) => {
  if (path && path.length >= 3) {
    form.device_type = path[2]
    
    // 获取驱动支持的 hardware_types
    const driver = driverList.value.find(d => d.type === form.device_type)
    if (driver?.hardware_types) {
      availableBusTypes.value = driver.hardware_types.map(b => {
        const opt = busOptions.find(o => o.value === b)
        return { value: b, label: opt?.label || b }
      })
      
      // 自动选择第一个支持的类型
      if (!availableBusTypes.value.find(b => b.value === form.hardware_type)) {
        form.hardware_type = availableBusTypes.value[0]?.value || 'uart'
      }
    }
  }
}

// 总线类型变化
const onHardwareTypeChange = () => {
  if (form.hardware_type !== 'uart') {
    form.protocol = ''
  } else {
    form.protocol = 'modbus'
  }
}

const handleSubmit = async () => {
  try {
    await formRef.value?.validate()
  } catch {
    ElMessage.error('请完善表单信息')
    return
  }

  submitting.value = true
  try {
    const submitData = {
      name: form.name,
      description: form.description,
      device_type: form.device_type,
      hardware_type: form.hardware_type,
      protocol: form.protocol || undefined,
      config: form.config,
      is_default: form.is_default
    }

    if (isEdit.value) {
      await deviceConfigApi.update(props.config!.id, submitData)
      ElMessage.success('更新成功')
    } else {
      await deviceConfigApi.create(submitData)
      ElMessage.success('创建成功')
    }

    emit('success')
    dialogVisible.value = false
  } catch (error: any) {
    ElMessage.error(error.message || '操作失败')
  } finally {
    submitting.value = false
  }
}

// 查找驱动路径 (3层: OEM → 种类 → 驱动)
const findDriverPath = (deviceType: string): string[] => {
  for (const oem of driverOptions.value) {
    for (const category of oem.children || []) {
      for (const driver of category.children || []) {
        if (driver.value === deviceType) {
          return [oem.value, category.value, driver.value]
        }
      }
    }
  }
  return []
}

watch(() => props.visible, (val) => {
  if (val) {
    if (props.config) {
      form.name = props.config.name
      form.description = props.config.description || ''
      form.device_type = props.config.device_type
      form.hardware_type = props.config.hardware_type
      form.protocol = props.config.protocol || 'modbus'
      form.is_default = props.config.is_default
      Object.assign(form.config, props.config.config || {})
      
      // 回显驱动选择
      form.driverPath = findDriverPath(form.device_type)
      onDriverChange(form.driverPath)
    } else {
      form.name = ''
      form.description = ''
      form.device_type = ''
      form.driverPath = []
      form.hardware_type = 'uart'
      form.protocol = 'modbus'
      form.is_default = false
    }
  }
})

onMounted(() => {
  loadDrivers()
})
</script>

<style scoped>
.config-form-dialog :deep(.el-dialog__body) {
  padding: 16px 20px;
  max-height: 70vh;
  overflow-y: auto;
}

.form-row {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}

.form-row .flex-1 {
  flex: 1;
}

.form-row .switch-item {
  width: 80px;
  flex-shrink: 0;
}

.form-row :deep(.el-form-item__content) {
  margin-left: 0 !important;
}

.driver-option {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}

.params-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}

.params-grid :deep(.el-form-item) {
  margin-bottom: 12px;
}

.params-grid :deep(.el-form-item__label) {
  font-size: 12px;
}

:deep(.el-divider__text) {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

@media (max-width: 768px) {
  .config-form-dialog :deep(.el-dialog) {
    width: 95% !important;
    margin: 10px auto;
    max-height: 90vh;
  }
  
  .form-row {
    flex-direction: column;
    gap: 0;
  }
  
  .form-row .switch-item {
    width: 100%;
  }
  
  .params-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
