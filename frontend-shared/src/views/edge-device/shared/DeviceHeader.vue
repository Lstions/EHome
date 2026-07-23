<template>
  <PageHeader :title="title" :show-back="true" @back="$emit('back')">
    <template #extra>
      <div class="header-actions">
        <el-tag :type="wsConnected ? 'success' : 'info'" size="default" class="ws-status-tag">
          <el-icon :size="14"><Connection /></el-icon>
          {{ wsConnected ? '实时连接' : '未连接' }}
        </el-tag>
        <div class="header-actions-group">
          <el-button :icon="Edit" @click="editDialogVisible = true">编辑</el-button>
          <el-button :icon="Connection" @click="$emit('syncToHA')" :loading="syncingHA"
            :disabled="!device || (device.status !== 'online' && device.status !== 'active')">
            同步到HA
          </el-button>
          <el-button v-if="canChangeAddress" type="warning" @click="showAddressDialog = true">
            修改地址
          </el-button>
          <el-tooltip v-else-if="device" content="该设备型号不支持地址修改" placement="top">
            <el-button type="warning" disabled>修改地址</el-button>
          </el-tooltip>
          <el-button v-if="device?.status === 'online' || device?.status === 'active'"
            type="primary" :icon="Refresh" :loading="refreshing" @click="$emit('refresh')">
            刷新数据
          </el-button>
        </div>
        <!-- 删除按钮单独隔离，避免误触 -->
        <el-button :icon="Delete" type="danger" plain @click="deleteDialogVisible = true">删除</el-button>
      </div>
    </template>
  </PageHeader>

  <!-- Edit dialog -->
  <el-dialog v-model="editDialogVisible" title="编辑边缘设备" width="500px" align-center class="dialog-mobile-constrained">
    <el-form :model="editForm" label-width="80px">
      <el-form-item label="设备名称">
        <el-input v-model="editForm.name" />
      </el-form-item>
      <el-form-item label="设备类型">
        <el-input :model-value="device?.device_type" disabled />
      </el-form-item>
      <el-form-item label="通信协议">
        <el-input :model-value="device?.protocol?.toUpperCase()" disabled />
      </el-form-item>
      <el-form-item label="硬件类型">
        <el-input :model-value="device?.hardware_type?.toUpperCase()" disabled />
      </el-form-item>
      <el-form-item label="硬件ID">
        <el-input :model-value="device?.hardware_id" disabled />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="editDialogVisible = false">取消</el-button>
      <el-button type="primary" @click="submitEdit" :loading="editLoading">保存</el-button>
    </template>
  </el-dialog>

  <!-- Delete dialog -->
  <el-dialog v-model="deleteDialogVisible" title="删除边缘设备" width="400px" align-center class="dialog-mobile-constrained">
    <p style="margin: 0;">
      确定要删除边缘设备 <strong>{{ device?.name }}</strong> 吗？此操作不可恢复。
    </p>
    <template #footer>
      <el-button @click="deleteDialogVisible = false">取消</el-button>
      <el-button type="danger" @click="submitDelete" :loading="deleteLoading">删除</el-button>
    </template>
  </el-dialog>

  <!-- Change address dialog -->
  <el-dialog v-model="showAddressDialog" title="修改设备地址" width="400px" align-center class="dialog-mobile-constrained">
    <el-form label-width="80px">
      <el-form-item label="当前地址">
        <el-tag size="small">{{ device?.hardware_id || 'N/A' }}</el-tag>
      </el-form-item>
      <el-form-item label="新地址" required>
        <el-input-number v-model="newAddress" :min="1" :max="247" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="showAddressDialog = false">取消</el-button>
      <el-button type="primary" :loading="changingAddress" @click="handleChangeAddress">确认修改</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, Connection, Edit, Delete } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'

const props = defineProps<{
  device: EdgeDevice | null
  wsConnected?: boolean
  syncingHA?: boolean
  refreshing?: boolean
  title?: string
}>()

const emit = defineEmits<{
  (e: 'back'): void
  (e: 'syncToHA'): void
  (e: 'refresh'): void
  (e: 'updated'): void
  (e: 'deleted'): void
}>()

const router = useRouter()
const edgeDeviceStore = useEdgeDeviceStore()

const editDialogVisible = ref(false)
const editLoading = ref(false)
const editForm = ref({ name: '' })

const deleteDialogVisible = ref(false)
const deleteLoading = ref(false)

const showAddressDialog = ref(false)
const newAddress = ref(1)
const changingAddress = ref(false)
let operationGeneration = 0

const canChangeAddress = computed(() => {
  const dc = props.device?.device_config
  if (!dc?.config) return false
  try {
    const cfg = typeof dc.config === 'string' ? JSON.parse(dc.config) : dc.config
    return !!cfg?.change_address_command
  } catch { return false }
})

// Watch: when editDialogVisible becomes true, sync the form
watch(editDialogVisible, (v) => {
  if (v) editForm.value.name = props.device?.name || ''
})

async function submitEdit() {
  if (!props.device) return
  const deviceId = props.device.id
  const operation = operationGeneration
  const generation = getSessionGeneration()
  editLoading.value = true
  try {
    await edgeDeviceApi.update(deviceId, { name: editForm.value.name })
    if (operation !== operationGeneration) return
    assertSessionGeneration(generation)
    if (props.device?.id !== deviceId) throw new Error('设备已变更')
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(deviceId)
    ElMessage.success('设备信息已保存')
    editDialogVisible.value = false
    emit('updated')
  } catch (e: any) {
    if (operation !== operationGeneration || props.device?.id !== deviceId) return
    ElMessage.error(e.message || '保存失败')
  } finally {
    if (props.device?.id === deviceId) editLoading.value = false
  }
}

async function submitDelete() {
  if (!props.device) return
  const deviceId = props.device.id
  const operation = operationGeneration
  deleteLoading.value = true
  try {
    await edgeDeviceStore.deleteDevice(deviceId)
    if (operation !== operationGeneration) return
    if (props.device?.id !== deviceId) throw new Error('设备已变更')
    ElMessage.success('设备已删除')
    deleteDialogVisible.value = false
    emit('deleted')
    router.replace('/edge-device')
  } catch (e: any) {
    if (operation !== operationGeneration || props.device?.id !== deviceId) return
    ElMessage.error(e.message || '删除失败')
  } finally {
    if (props.device?.id === deviceId) deleteLoading.value = false
  }
}

async function handleChangeAddress() {
  if (!props.device) return
  const deviceId = props.device.id
  const operation = operationGeneration
  const generation = getSessionGeneration()
  changingAddress.value = true
  try {
    await edgeDeviceApi.changeAddress(deviceId, newAddress.value)
    if (operation !== operationGeneration) return
    assertSessionGeneration(generation)
    if (props.device?.id !== deviceId) throw new Error('设备已变更')
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(deviceId)
    ElMessage.success(`地址已修改为 ${newAddress.value}`)
    showAddressDialog.value = false
    emit('updated')
  } catch (e: any) {
    if (operation !== operationGeneration || props.device?.id !== deviceId) return
    ElMessage.error('修改失败: ' + (e.message || '未知错误'))
  } finally {
    if (props.device?.id === deviceId) changingAddress.value = false
  }
}

onUnmounted(() => {
  operationGeneration++
})
</script>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.header-actions-group {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.ws-status-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 88px;
  justify-content: center;
}
@media (max-width: 1100px) {
  .header-actions,
  .header-actions-group {
    width: 100%;
  }
}
@media (max-width: 768px) {
  .header-actions {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
  .header-actions-group {
    width: 100%;
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  .header-actions-group > * {
    min-width: 0;
  }
  .header-actions-group :deep(.el-button) {
    width: 100%;
    min-height: 40px;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .header-actions-group .ws-status-tag {
    width: 100%;
    min-height: 32px;
  }
  .header-actions > .ws-status-tag {
    width: 100%;
    justify-content: flex-start;
    min-height: 32px;
    margin: 0;
  }
  .header-actions > .el-button {
    width: 100%;
    min-height: 40px;
    margin: 0;
  }
}
</style>
