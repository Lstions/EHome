<template>
  <el-dialog v-model="dialogVisible" :title="`配置外设设备 — ${peripheralLabel}`" width="600px" :close-on-click-modal="false">
    <el-form :model="form" :rules="rules" label-width="100px" ref="formRef">
      <el-form-item label="设备类型" prop="device_type">
        <el-select v-model="form.device_type" placeholder="请选择设备类型" style="width:100%" @change="handleDeviceTypeChange">
          <el-option v-for="d in deviceTypeOptions" :key="d.value" :label="d.label" :value="d.value" />
        </el-select>
      </el-form-item>
      <el-form-item label="设备名称" prop="device_name">
        <el-input v-model="form.device_name" placeholder="请输入设备名称" />
      </el-form-item>
      <el-form-item label="配置模板">
        <el-select v-model="form.template_id" placeholder="选择配置模板" style="width:100%" clearable :loading="templatesLoading" @change="handleTemplateChange">
          <el-option v-for="t in templates" :key="t.id" :label="t.name+(t.is_default?' (默认)':'')" :value="t.id">
            <div style="display:flex;justify-content:space-between"><span>{{t.name}}</span><el-tag v-if="t.is_default" type="success" size="small">默认</el-tag></div>
          </el-option>
        </el-select>
      </el-form-item>
      <el-form-item label="通信协议" prop="protocol">
        <el-select v-model="form.protocol" placeholder="请选择通信协议" style="width:100%">
          <el-option label="MODBUS RTU" value="modbus" />
          <el-option label="字节流" value="stream" />
        </el-select>
      </el-form-item>
      <template v-if="form.protocol==='modbus'">
        <el-divider content-position="left">MODBUS RTU 参数</el-divider>
        <el-form-item label="从机地址" prop="config.modbus_address">
          <el-input-number v-model.number="form.config.modbus_address" :min="1" :max="247" style="width:100%" />
        </el-form-item>
        <el-form-item label="波特率">
          <el-select v-model="form.config.baudrate" style="width:100%">
            <el-option v-for="b in baudOptions" :key="b" :label="String(b)" :value="b" />
          </el-select>
        </el-form-item>
        <el-form-item label="数据位">
          <el-select v-model="form.config.data_bits" style="width:100%">
            <el-option :value="7" label="7" />
            <el-option :value="8" label="8" />
          </el-select>
        </el-form-item>
        <el-form-item label="停止位">
          <el-select v-model="form.config.stop_bits" style="width:100%">
            <el-option :value="1" label="1" />
            <el-option :value="2" label="2" />
          </el-select>
        </el-form-item>
        <el-form-item label="校验位">
          <el-select v-model="form.config.parity" style="width:100%">
            <el-option value="none" label="无校验 (None)" />
            <el-option value="even" label="偶校验 (Even)" />
            <el-option value="odd" label="奇校验 (Odd)" />
          </el-select>
        </el-form-item>
      </template>
      <template v-if="form.protocol==='stream'">
        <el-divider content-position="left">字节流参数</el-divider>
        <el-form-item label="波特率">
          <el-select v-model="form.config.baudrate" style="width:100%">
            <el-option v-for="b in baudOptions" :key="b" :label="String(b)" :value="b" />
          </el-select>
        </el-form-item>
      </template>
    </el-form>
    <template #footer>
      <el-button @click="dialogVisible=false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">确定分配</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { nodeApi, type PeripheralAssignment } from '@/api/node'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'

const props = defineProps<{
  visible: boolean
  collectorId: string
  peripheralType: 'uart'|'i2c'|'spi'
  peripheralId: string
  peripheralMode?: string
}>()
const emit = defineEmits<{
  (e: 'success'): void
  (e: 'update:visible', v: boolean): void
}>()

const formRef = ref()
const submitting = ref(false)
const templates = ref<DeviceConfig[]>([])
const templatesLoading = ref(false)
const baudOptions = [1200,2400,4800,9600,19200,38400,57600,115200]

const peripheralLabel = computed(() => {
  const typeNames: Record<string, string> = { uart: 'UART', i2c: 'I2C', spi: 'SPI' }
  let label = `${typeNames[props.peripheralType] || props.peripheralType} — ${props.peripheralId}`
  if (props.peripheralMode) label += ` (${props.peripheralMode})`
  return label
})

const dialogVisible = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v)
})

const form = reactive({
  device_type: '',
  device_name: '',
  protocol: 'modbus' as 'modbus'|'stream',
  template_id: undefined as number|undefined,
  config: {
    modbus_address: 1,
    baudrate: 9600,
    data_bits: 8,
    stop_bits: 1,
    parity: 'none'
  } as Record<string, any>
})

const deviceTypeOptions = [
  {value:'wind_speed', label:'风速传感器'},
  {value:'wind_direction', label:'风向传感器'},
  {value:'rain', label:'雨量传感器'},
  {value:'light', label:'光照传感器'},
  {value:'temp_humidity', label:'温湿度传感器'},
  {value:'battery', label:'电池保护板'},
  {value:'jiabaida_bms', label:'BMS 电池管理系统'},
  {value:'inverter', label:'光伏逆变器'}
]

const rules = {
  device_type: [{required: true, message: '请选择设备类型', trigger: 'change'}],
  device_name: [{required: true, message: '请输入设备名称', trigger: 'blur'}],
  protocol: [{required: true, message: '请选择通信协议', trigger: 'change'}]
}

const loadTemplates = async (dt: string) => {
  if (!dt) { templates.value = []; return }
  templatesLoading.value = true
  try { templates.value = await deviceConfigApi.getByDeviceType(dt) }
  catch { templates.value = [] }
  finally { templatesLoading.value = false }
}

const handleDeviceTypeChange = async (v: string) => {
  form.template_id = undefined
  if (v && !form.device_name) {
    const opt = deviceTypeOptions.find(d => d.value === v)
    form.device_name = opt?.label || v
  }
  form.protocol = ['battery','light','rain'].includes(v) ? 'stream' : 'modbus'
  await loadTemplates(v)
  const defT = templates.value.find(t => t.is_default)
  if (defT) { form.template_id = defT.id; applyTemplate(defT) }
}

const handleTemplateChange = (tid: number|undefined) => {
  if (!tid) { resetConfig(); return }
  const t = templates.value.find(t => t.id === tid)
  if (t) applyTemplate(t)
}

const applyTemplate = (t: DeviceConfig) => {
  const c = t.config || {}
  if (t.protocol) form.protocol = t.protocol
  if (c.baudrate != null) form.config.baudrate = c.baudrate
  if (c.data_bits != null) form.config.data_bits = c.data_bits
  if (c.stop_bits != null) form.config.stop_bits = c.stop_bits
  if (c.parity) form.config.parity = c.parity
  if (c.modbus_address != null) form.config.modbus_address = c.modbus_address
}

const resetConfig = () => {
  form.config = { modbus_address: 1, baudrate: 9600, data_bits: 8, stop_bits: 1, parity: 'none' }
}

const handleSubmit = async () => {
  const ok = await formRef.value?.validate().catch(() => false)
  if (!ok) return
  submitting.value = true
  try {
    const configData = form.protocol === 'modbus'
      ? { modbus_address: form.config.modbus_address, baudrate: form.config.baudrate, data_bits: form.config.data_bits, stop_bits: form.config.stop_bits, parity: form.config.parity }
      : { baudrate: form.config.baudrate }
    const assignment: PeripheralAssignment = {
      peripheral_type: props.peripheralType,
      peripheral_id: props.peripheralId,
      device_type: form.device_type,
      device_name: form.device_name,
      protocol: form.protocol,
      template_id: form.template_id,
      config: configData
    }
    await nodeApi.assignPeripheral(props.collectorId, assignment)
    ElMessage.success('外设分配成功')
    emit('success')
    dialogVisible.value = false
  } catch (e: any) {
    ElMessage.error(e?.message || '外设分配失败')
  } finally {
    submitting.value = false
  }
}

watch(() => props.visible, (v) => {
  if (v) {
    form.device_type = ''
    form.device_name = ''
    form.protocol = 'modbus'
    form.template_id = undefined
    resetConfig()
    templates.value = []
    formRef.value?.resetFields()
  }
})
</script>

<style scoped>
</style>
