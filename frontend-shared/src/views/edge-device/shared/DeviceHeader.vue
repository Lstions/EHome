<template>
  <PageHeader :title="title" :show-back="true" @back="$emit('back')">
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

  <!-- Change address dialog -->
  <el-dialog v-model="showAddressDialog" title="修改设备地址" width="400px" align-center>
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
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh, Connection, Edit, Delete } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'

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

const editDialogVisible = ref(false)
const editLoading = ref(false)
const editForm = ref({ name: '' })

const deleteDialogVisible = ref(false)
const deleteLoading = ref(false)

const showAddressDialog = ref(false)
const newAddress = ref(1)
const changingAddress = ref(false)

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
  editLoading.value = true
  try {
    await edgeDeviceApi.update(props.device.id, { name: editForm.value.name })
    ElMessage.success('设备信息已保存')
    editDialogVisible.value = false
    emit('updated')
  } catch (e: any) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    editLoading.value = false
  }
}

async function submitDelete() {
  if (!props.device) return
  deleteLoading.value = true
  try {
    await edgeDeviceApi.delete(props.device.id)
    ElMessage.success('设备已删除')
    deleteDialogVisible.value = false
    emit('deleted')
    router.replace('/edge-device')
  } catch (e: any) {
    ElMessage.error(e.message || '删除失败')
  } finally {
    deleteLoading.value = false
  }
}

async function handleChangeAddress() {
  if (!props.device) return
  changingAddress.value = true
  try {
    await edgeDeviceApi.changeAddress(props.device.id, newAddress.value)
    ElMessage.success(`地址已修改为 ${newAddress.value}`)
    showAddressDialog.value = false
    emit('updated')
  } catch (e: any) {
    ElMessage.error('修改失败: ' + (e.message || '未知错误'))
  } finally {
    changingAddress.value = false
  }
}
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
</style>
