<template>
  <el-card style="margin-top: 20px;" shadow="hover" v-if="hasConfigOperations">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>设备操作</span>
        <el-tag v-if="isDeviceOffline" type="info" size="small">设备离线，操作已禁用</el-tag>
      </div>
    </template>
    <div class="operation-buttons">
      <template v-for="(op, opKey) in configOperations" :key="opKey">
        <el-button
          v-if="!op.params || op.params.length === 0"
          :type="op.type === 'read' ? 'primary' : 'warning'"
          :loading="operationLoading[opKey]"
          :disabled="isDeviceOffline"
          @click="executeConfigOperation(opKey, op)"
        >{{ op.label }}</el-button>
        <el-button
          v-else
          :type="op.type === 'read' ? 'primary' : 'warning'"
          :loading="operationLoading[opKey]"
          :disabled="isDeviceOffline"
          @click="openOperationDialog(opKey, op)"
        >{{ op.label }}</el-button>
      </template>
    </div>
  </el-card>

  <!-- Operation params dialog -->
  <el-dialog v-model="opDialogVisible" :title="opDialogTitle" width="420px" align-center>
    <el-form ref="opFormRef" :model="opParamValues" label-width="100px">
      <el-form-item
        v-for="param in opDialogParams"
        :key="param.name"
        :label="param.label || param.name"
        required
      >
        <el-input-number
          v-if="isNumericType(param.type)"
          v-model="opParamValues[param.name]"
          :min="param.min ?? 0"
          :max="param.max ?? getDefaultMax(param.type)"
          :step="param.step ?? 1"
          style="width: 100%;"
        />
        <el-select
          v-else-if="param.type === 'enum'"
          v-model="opParamValues[param.name]"
          placeholder="请选择"
          style="width: 100%;"
        >
          <el-option
            v-for="opt in param.options"
            :key="opt.value"
            :label="opt.label"
            :value="opt.value"
          />
        </el-select>
        <el-input
          v-else
          v-model="opParamValues[param.name]"
          :placeholder="`请输入${param.label || param.name}`"
        />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="opDialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="opDialogLoading" @click="submitOperationDialog">确定</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance } from 'element-plus'
import { edgeDeviceApi, type EdgeDevice, type ExecuteOperationResponse } from '@/api/edgeDevice'
import { type OperationDef, type OperationParam } from '@/api/deviceConfig'

const props = defineProps<{
  device: EdgeDevice | null
  deviceId: number
}>()

const emit = defineEmits<{
  (e: 'operationExecuted'): void
}>()

const configOperations = computed<Record<string, OperationDef>>(() => {
  const dc = props.device?.device_config
  if (!dc) return {}
  let ops = dc.operations
  if (!ops && dc.config) {
    try {
      const cfg = typeof dc.config === 'string' ? JSON.parse(dc.config) : dc.config
      ops = cfg?.operations
    } catch { return {} }
  }
  if (!ops || typeof ops !== 'object') return {}
  return ops as Record<string, OperationDef>
})

const hasConfigOperations = computed(() => Object.keys(configOperations.value).length > 0)

const isDeviceOffline = computed(() => {
  if (!props.device) return true
  return props.device.status !== 'online' && props.device.status !== 'active'
})

const operationLoading = reactive<Record<string, boolean>>({})
const opDialogVisible = ref(false)
const opDialogTitle = ref('')
const opDialogParams = ref<OperationParam[]>([])
const opParamValues = ref<Record<string, number | string>>({})
const opDialogLoading = ref(false)
const currentOpKey = ref('')
const opFormRef = ref<FormInstance>()

function isNumericType(type: string): boolean {
  return ['uint8', 'uint16', 'int8', 'int16', 'int32', 'uint32', 'float'].includes(type)
}

function getDefaultMax(type: string): number {
  const map: Record<string, number> = {
    uint8: 255, uint16: 65535, int8: 127, int16: 32767, int32: 2147483647, uint32: 4294967295
  }
  return map[type] ?? 255
}

function openOperationDialog(opKey: string, op: OperationDef) {
  currentOpKey.value = opKey
  opDialogTitle.value = op.label
  opDialogParams.value = op.params || []
  const defaults: Record<string, number | string> = {}
  for (const p of op.params || []) {
    if (p.type === 'enum' && p.options && p.options.length > 0) {
      defaults[p.name] = p.options[0].value
    } else if (isNumericType(p.type)) {
      defaults[p.name] = p.min ?? (p.default !== undefined ? p.default : 0)
    } else if (p.default !== undefined) {
      defaults[p.name] = p.default
    } else {
      defaults[p.name] = ''
    }
  }
  opParamValues.value = defaults
  opDialogVisible.value = true
}

async function submitOperationDialog() {
  const opKey = currentOpKey.value
  const op = configOperations.value[opKey]
  if (!op || !props.device) return

  if (opFormRef.value) {
    try { await opFormRef.value.validate() } catch { return }
  }

  opDialogLoading.value = true
  operationLoading[opKey] = true
  try {
    const id = props.deviceId
    const result = await edgeDeviceApi.executeOperation(id, opKey, opParamValues.value)
    await handleOperationResult(opKey, op, result)
    opDialogVisible.value = false
  } catch (error: any) {
    ElMessage.error(error.message || '操作执行失败')
  } finally {
    opDialogLoading.value = false
    operationLoading[opKey] = false
  }
}

async function executeConfigOperation(opKey: string, op: OperationDef) {
  if (!props.device) return
  if (op.type === 'write') {
    try {
      await ElMessageBox.confirm(`确定要执行"${op.label}"操作吗？`, '确认操作',
        { confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning' })
    } catch { return }
  }
  operationLoading[opKey] = true
  try {
    const id = props.deviceId
    const result = await edgeDeviceApi.executeOperation(id, opKey)
    await handleOperationResult(opKey, op, result)
  } catch (error: any) {
    ElMessage.error(error.message || '操作执行失败')
  } finally {
    operationLoading[opKey] = false
  }
}

async function handleOperationResult(opKey: string, op: OperationDef, result: ExecuteOperationResponse) {
  if (op.type === 'write') {
    ElMessage.success('命令已发送')
    emit('operationExecuted')
  } else {
    const value = result?.value ?? result?.data?.value
    const unit = result?.unit ?? result?.data?.unit ?? ''
    if (value !== undefined && value !== null) {
      ElMessage.success(`查询结果: ${value}${unit ? ' ' + unit : ''}`)
    } else {
      ElMessage.success('查询成功')
    }
  }
}
</script>

<style scoped>
.operation-buttons { display: flex; gap: 12px; flex-wrap: wrap; }
</style>
