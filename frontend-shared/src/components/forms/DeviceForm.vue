import { logger } from '@/utils/logger'
<!-- @deprecated 此组件已废弃，请使用 DeviceList.vue 中的创建向导 -->
<template>
  <el-dialog
    v-model="dialogVisible"
    :title="isEdit ? '编辑设备' : '创建设备'"
    width="600px"
    @close="handleClose"
  >
    <el-form :model="form" :rules="rules" label-width="120px" ref="formRef">
      <el-form-item label="所属采集器" prop="collector_id">
        <el-input-number
          v-model="form.collector_id"
          :min="1"
          placeholder="请输入采集器ID"
          style="width: 100%;"
        />
      </el-form-item>

      <el-form-item label="设备名称" prop="name">
        <el-input
          v-model="form.name"
          placeholder="请输入设备名称"
          clearable
        />
      </el-form-item>

      <el-form-item label="设备类型" prop="device_type">
        <el-select
          v-model="form.device_type"
          placeholder="请选择设备类型"
          style="width: 100%;"
        >
          <el-option label="风速传感器" value="wind_speed" />
          <el-option label="风向传感器" value="wind_direction" />
          <el-option label="雨量传感器" value="rain" />
          <el-option label="光照传感器" value="light" />
          <el-option label="温湿度传感器" value="temp_humidity" />
          <el-option label="电池保护板" value="battery" />
          <el-option label="光伏逆变器" value="inverter" />
        </el-select>
      </el-form-item>

      <el-form-item label="通信协议" prop="protocol">
        <el-radio-group v-model="form.protocol">
          <el-radio value="modbus">MODBUS</el-radio>
          <el-radio value="stream">字节流</el-radio>
        </el-radio-group>
      </el-form-item>

      <el-form-item label="硬件类型" prop="hardware_type">
        <el-radio-group v-model="form.hardware_type">
          <el-radio value="uart">UART</el-radio>
          <el-radio value="i2c">I2C</el-radio>
          <el-radio value="spi">SPI</el-radio>
        </el-radio-group>
      </el-form-item>

      <el-form-item label="硬件ID" prop="hardware_id">
        <el-input
          v-model="form.hardware_id"
          placeholder="如: UART1"
          clearable
        />
      </el-form-item>

      <el-form-item label="MODBUS地址" prop="modbus_address" v-if="form.protocol === 'modbus'">
        <el-input-number
          v-model="form.modbus_address"
          :min="1"
          :max="247"
          placeholder="1-247"
          style="width: 100%;"
        />
      </el-form-item>

      <el-form-item label="波特率" prop="baudrate" v-if="form.protocol === 'modbus'">
        <el-select v-model="form.baudrate" placeholder="请选择波特率" style="width: 100%;">
          <el-option label="300" :value="300" />
          <el-option label="600" :value="600" />
          <el-option label="1200" :value="1200" />
          <el-option label="2400" :value="2400" />
          <el-option label="4800" :value="4800" />
          <el-option label="9600" :value="9600" />
          <el-option label="19200" :value="19200" />
          <el-option label="38400" :value="38400" />
          <el-option label="57600" :value="57600" />
          <el-option label="115200" :value="115200" />
        </el-select>
      </el-form-item>

      <el-form-item label="数据位" prop="data_bits" v-if="form.protocol === 'modbus'">
        <el-select v-model="form.data_bits" placeholder="请选择数据位" style="width: 100%;">
          <el-option label="8" :value="8" />
          <el-option label="7" :value="7" />
        </el-select>
      </el-form-item>

      <el-form-item label="校验位" prop="parity" v-if="form.protocol === 'modbus'">
        <el-select v-model="form.parity" placeholder="请选择校验位" style="width: 100%;">
          <el-option label="无" value="none" />
          <el-option label="奇校验" value="odd" />
          <el-option label="偶校验" value="even" />
        </el-select>
      </el-form-item>

      <el-form-item label="停止位" prop="stop_bits" v-if="form.protocol === 'modbus'">
        <el-select v-model="form.stop_bits" placeholder="请选择停止位" style="width: 100%;">
          <el-option label="1" :value="1" />
          <el-option label="2" :value="2" />
        </el-select>
      </el-form-item>

      <el-divider content-position="left">高级配置（JSON）</el-divider>

      <el-form-item>
        <el-input
          v-model="form.config"
          type="textarea"
          :rows="4"
          placeholder='设备配置（JSON格式），如：{"registers": [{"address": 0, "length": 2}]}'
        />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="handleClose">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">
        {{ isEdit ? '保存' : '创建' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch, computed } from 'vue'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

const props = withDefaults(defineProps<{
  visible: boolean
  deviceId?: number | null
  collectorId?: number | null
}>(), {
  visible: false,
  deviceId: null,
  collectorId: null
})

const emit = defineEmits<{
  (e: 'success', device: any): void
  (e: 'update', device: any): void
}>()

const formRef = ref<FormInstance>()
const submitting = ref(false)

const isEdit = computed(() => !!props.deviceId)

const form = reactive({
  collector_id: props.collectorId || 1,
  name: '',
  device_type: '',
  protocol: 'modbus',
  hardware_type: 'uart',
  hardware_id: '',
  modbus_address: 1,
  baudrate: 9600,
  data_bits: 8,
  parity: 'none',
  stop_bits: 1,
  config: '{}'
})

const rules: FormRules = {
  collector_id: [{ required: true, message: '请输入采集器ID', trigger: 'blur' }],
  name: [{ required: true, message: '请输入设备名称', trigger: 'blur' }],
  device_type: [{ required: true, message: '请选择设备类型', trigger: 'change' }],
  protocol: [{ required: true, message: '请选择通信协议', trigger: 'change' }],
  hardware_type: [{ required: true, message: '请选择硬件类型', trigger: 'change' }],
  hardware_id: [{ required: true, message: '请输入硬件ID', trigger: 'blur' }],
  modbus_address: [
    { required: true, message: '请输入MODBUS地址', trigger: 'blur' }
  ],
  baudrate: [{ required: true, message: '请选择波特率', trigger: 'change' }],
  config: [
    { required: true, message: '请输入设备配置', trigger: 'blur' },
    {
      validator: (rule, value, callback) => {
        try {
          JSON.parse(value)
          callback()
        } catch {
          callback(new Error('请输入有效的JSON格式'))
        }
      }
    }
  ]
}

const dialogVisible = computed({
  get: () => props.visible,
  set: (val) => {
    if (!val) {
      resetForm()
    }
  }
})

const resetForm = () => {
  formRef.value?.resetFields()
  form.collector_id = props.collectorId || 1
  form.name = ''
  form.device_type = ''
  form.protocol = 'modbus'
  form.hardware_type = 'uart'
  form.hardware_id = ''
  form.modbus_address = 1
  form.baudrate = 9600
  form.data_bits = 8
  form.parity = 'none'
  form.stop_bits = 1
  form.config = '{}'
}

const handleSubmit = async () => {
  if (!formRef.value) return

  await formRef.value.validate((valid) => {
    if (valid) {
      submitting.value = true

      // 验证 config JSON
      let config: Record<string, any> = {}
      if (form.config && form.config.trim()) {
        try {
          config = JSON.parse(form.config)
        } catch {
          ElMessage.error('配置JSON格式错误')
          submitting.value = false
          return
        }
      }

      const deviceData = {
        collector_id: form.collector_id,
        name: form.name,
        device_type: form.device_type,
        protocol: form.protocol,
        hardware_type: form.hardware_type,
        hardware_id: form.hardware_id,
        config: {
          modbus_address: form.modbus_address,
          baudrate: form.baudrate,
          data_bits: form.data_bits,
          parity: form.parity,
          stop_bits: form.stop_bits,
          ...config
        }
      }

      if (isEdit.value) {
        // 编辑模式 - 调用更新 API
        try {
          const { edgeDeviceApi } = await import('@/api/device')
          await edgeDeviceApi.update(props.deviceId!, {
            name: form.name,
            device_type: form.device_type,
            protocol: form.protocol,
            hardware_type: form.hardware_type,
            hardware_id: form.hardware_id,
            config: JSON.parse(form.config)
          })
          emit('update', { id: props.deviceId, ...form })
        } catch (error) {
          ElMessage.error('更新失败')
        }
      } else {
        emit('success', deviceData)
      }
    }
  })
}

const handleClose = () => {
  dialogVisible.value = false
}

// 监听 visible 变化，重置表单
watch(() => props.visible, (newVal) => {
  if (!newVal) {
    resetForm()
  }
})

// 如果传入了设备ID，则加载设备信息
watch(() => props.deviceId, async (newVal) => {
  if (newVal && props.visible) {
    try {
      const { edgeDeviceApi } = await import('@/api/device')
      const device = await edgeDeviceApi.getDetail(newVal)
      form.collector_id = device.collector_id
      form.name = device.name
      form.device_type = device.device_type
      form.protocol = device.protocol
      form.hardware_type = device.hardware_type
      form.hardware_id = device.hardware_id
      form.config = JSON.stringify(device.config || {}, null, 2)
      // 从 config 中提取 interval_ms 等字段
      if (device.config?.interval_ms) {
        form.modbus_address = device.config.modbus_address || 1
        form.baudrate = device.config.baudrate || 9600
        form.data_bits = device.config.data_bits || 8
        form.parity = device.config.parity || 'none'
        form.stop_bits = device.config.stop_bits || 1
      }
    } catch (error) {
      logger.error('加载设备详情失败', { error })
      ElMessage.error('加载设备详情失败')
    }
  }
})
</script>

<style scoped>
.el-divider {
  margin: 16px 0;
}
</style>
