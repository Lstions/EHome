<template>
  <PageHeader class="device-page-header" :title="title" :show-back="true" @back="$emit('back')">
    <template #extra>
      <div class="header-actions">
        <div class="header-actions-group">
          <el-tag v-if="wsConnected" type="success" size="large">
            <el-icon :size="16"><Connection /></el-icon>
            实时连接
          </el-tag>
          <!-- 桌面端：编辑/同步到HA 横排 -->
          <template v-if="!isMobile">
            <el-button :icon="Edit" @click="editDialogVisible = true">编辑</el-button>
            <el-button :icon="Connection" @click="$emit('syncToHA')" :loading="syncingHA"
              :disabled="!device || (device.status !== 'online' && device.status !== 'active')">
              同步到HA
            </el-button>
          </template>
          <!-- 主操作：刷新数据（桌面/移动端均保留） -->
          <el-button v-if="device?.status === 'online' || device?.status === 'active'"
            type="primary" :icon="Refresh" :loading="refreshing" @click="$emit('refresh')">
            刷新数据
          </el-button>
          <!-- 移动端：编辑/同步到HA/删除 收进溢出下拉，消除按钮群挤压标题 -->
          <el-dropdown v-if="isMobile" trigger="click" popper-class="device-header-more-popover"
            @command="handleMoreCommand">
            <el-button :icon="MoreFilled" circle aria-label="更多操作" />
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="edit" :icon="Edit">编辑</el-dropdown-item>
                <el-dropdown-item command="syncToHA" :icon="Connection"
                  :disabled="!device || (device.status !== 'online' && device.status !== 'active')">
                  同步到HA
                </el-dropdown-item>
                <el-dropdown-item command="delete" :icon="Delete" divided>
                  <span style="color: var(--el-color-danger);">删除</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        <!-- 桌面端：删除按钮单独隔离，避免误触 -->
        <template v-if="!isMobile">
          <el-divider direction="vertical" class="action-divider" />
          <el-button :icon="Delete" type="danger" plain @click="deleteDialogVisible = true">删除</el-button>
        </template>
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
import { Refresh, Connection, Edit, Delete, MoreFilled } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { useResponsive } from '@/composables/useResponsive'
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

// 移动端操作收敛：≤768px 时编辑/同步到HA/删除收进溢出下拉
const { isMobile } = useResponsive()

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

/** 移动端溢出下拉命令分发：与桌面端按钮行为保持一致 */
function handleMoreCommand(command: string) {
  if (command === 'edit') editDialogVisible.value = true
  else if (command === 'syncToHA') emit('syncToHA')
  else if (command === 'delete') deleteDialogVisible.value = true
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
  /* 移动端仅保留 实时连接/刷新数据/⋯溢出下拉，按钮紧凑小尺寸；圆形更多按钮保持正圆 */
  .header-actions :deep(.el-button) {
    height: 28px;
    padding: 5px 11px;
    font-size: 12px;
  }
  .header-actions :deep(.el-button.is-circle) {
    width: 28px;
    padding: 0;
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

/* el-dropdown 弹出层 teleport 到 body，scoped 样式不生效，需 :global 包裹；
   参照 theme.css .notification-popover 的限宽写法（.el-dialog 的 92vw 兜底不覆盖 popover），防窄屏溢出 */
:global(.device-header-more-popover) {
  max-width: calc(100vw - 24px);
  box-sizing: border-box;
}
</style>
