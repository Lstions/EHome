<template>
  <PageHeader class="device-page-header" :title="title" :show-back="true" @back="$emit('back')">
    <template #extra>
      <div class="header-actions">
        <div class="header-actions-group">
          <el-tag v-if="wsConnected" type="success" size="large">
            <el-icon :size="16"><Connection /></el-icon>
            实时连接
          </el-tag>
          <el-button :icon="Edit" @click="editDialogVisible = true">编辑</el-button>
          <el-button :icon="Connection" @click="$emit('syncToHA')" :loading="syncingHA"
            :disabled="!device || (device.status !== 'online' && device.status !== 'active')">
            同步到HA
          </el-button>
          <el-button v-if="device?.status === 'online' || device?.status === 'active'"
            type="primary" :icon="Refresh" :loading="refreshing" @click="$emit('refresh')">
            刷新数据
          </el-button>
        </div>
        <!-- 删除按钮单独隔离，避免误触 -->
        <el-divider direction="vertical" class="action-divider" />
        <el-button :icon="Delete" type="danger" plain @click="deleteDialogVisible = true">删除</el-button>
      </div>
    </template>
  </PageHeader>

  <!-- Edit dialog -->
  <el-dialog v-model="editDialogVisible" title="编辑边缘设备" width="500px">
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
  <el-dialog v-model="deleteDialogVisible" title="删除边缘设备" width="400px">
    <p style="margin: 0;">
      确定要删除边缘设备 <strong>{{ device?.name }}</strong> 吗？此操作不可恢复。
    </p>
    <template #footer>
      <el-button @click="deleteDialogVisible = false">取消</el-button>
      <el-button type="danger" @click="submitDelete" :loading="deleteLoading">删除</el-button>
    </template>
  </el-dialog>

</template>

<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
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

let operationGeneration = 0

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
}
.header-actions-group {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.action-divider {
  margin: 0 4px;
}

/* 移动端：按钮组统一小尺寸、等宽换行排列；标题限制两行；隐藏换行后无意义的竖分隔符 */
@media (max-width: 768px) {
  /* 长标题最多两行，避免折行过多挤压右侧按钮区 */
  .device-page-header :deep(.page-header-left) {
    min-width: 0;
  }
  .device-page-header :deep(.page-header-left h2) {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    word-break: break-word;
  }

  .header-actions {
    gap: 8px;
    width: 100%;
  }
  /* 扁平化内层分组，让删除按钮与主按钮组参与同一 flex 换行流，行间左缘对齐 */
  .header-actions-group {
    display: contents;
  }
  /* 按钮换行后竖分隔符会变成孤立的"|"，移动端隐藏 */
  .action-divider {
    display: none;
  }
  .header-actions :deep(.el-button) {
    flex: 1 1 auto;
    height: 28px;
    padding: 5px 11px;
    font-size: 12px;
  }
  .header-actions :deep(.el-button + .el-button) {
    margin-left: 0;
  }
  .header-actions :deep(.el-tag) {
    height: 28px;
    padding: 0 8px;
    font-size: 12px;
  }
}
</style>
