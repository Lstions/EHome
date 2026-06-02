<template>
  <div class="data-source-manage">
    <div class="page-header">
      <h2>数据源管理</h2>
    </div>

    <el-card>
      <div class="filter-bar">
        <el-select v-model="filterDeviceId" placeholder="选择设备" clearable @change="loadDataSources">
          <el-option v-for="device in devices" :key="device.id" :label="device.name" :value="device.id" />
        </el-select>
        <el-select v-model="filterStatus" placeholder="状态" clearable @change="loadDataSources">
          <el-option label="正常" value="active" />
          <el-option label="待命" value="standby" />
          <el-option label="故障" value="error" />
          <el-option label="禁用" value="disabled" />
        </el-select>
      </div>

      <el-table :data="dataSources" v-loading="loading" stripe>
        <el-table-column prop="id" label="ID" width="80" />
        <el-table-column prop="device_id" label="设备ID" width="100" />
        <el-table-column prop="category" label="数据类别" />
        <el-table-column prop="source_type" label="来源类型" />
        <el-table-column prop="collector_id" label="采集器ID" width="100" />
        <el-table-column prop="channel_id" label="通道ID" width="100" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="getStatusType(row.status)">
              {{ getStatusLabel(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="priority" label="优先级" width="80" />
        <el-table-column prop="last_data_time" label="最后数据时间" width="180" />
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button size="small" @click="showDetail(row)">详情</el-button>
            <el-button size="small" type="danger" @click="handleDisable(row)">禁用</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pagination">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          @size-change="loadDataSources"
          @current-change="loadDataSources"
        />
      </div>
    </el-card>

    <!-- 详情对话框 -->
    <el-dialog v-model="detailVisible" title="数据源详情" width="600px">
      <el-descriptions :column="2" border>
        <el-descriptions-item label="ID">{{ currentSource?.id }}</el-descriptions-item>
        <el-descriptions-item label="设备ID">{{ currentSource?.device_id }}</el-descriptions-item>
        <el-descriptions-item label="数据类别">{{ currentSource?.category }}</el-descriptions-item>
        <el-descriptions-item label="来源类型">{{ currentSource?.source_type }}</el-descriptions-item>
        <el-descriptions-item label="采集器ID">{{ currentSource?.collector_id }}</el-descriptions-item>
        <el-descriptions-item label="通道ID">{{ currentSource?.channel_id }}</el-descriptions-item>
        <el-descriptions-item label="状态">{{ getStatusLabel(currentSource?.status) }}</el-descriptions-item>
        <el-descriptions-item label="优先级">{{ currentSource?.priority }}</el-descriptions-item>
        <el-descriptions-item label="配置" :span="2">{{ currentSource?.config }}</el-descriptions-item>
        <el-descriptions-item label="最后数据时间" :span="2">{{ currentSource?.last_data_time }}</el-descriptions-item>
      </el-descriptions>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import axios from 'axios'

interface DataSource {
  id: number
  device_id: number
  category: string
  source_type: string
  collector_id: number
  channel_id: number
  status: string
  priority: number
  config: string
  last_data_time: string
}

interface Device {
  id: number
  name: string
}

const dataSources = ref<DataSource[]>([])
const devices = ref<Device[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)

const filterDeviceId = ref<number | undefined>(undefined)
const filterStatus = ref('')

const detailVisible = ref(false)
const currentSource = ref<DataSource | null>(null)

const getStatusType = (status?: string) => {
  const map: Record<string, string> = {
    active: 'success',
    standby: 'warning',
    error: 'danger',
    disabled: 'info'
  }
  return map[status || ''] || 'info'
}

const getStatusLabel = (status?: string) => {
  const map: Record<string, string> = {
    active: '正常',
    standby: '待命',
    error: '故障',
    disabled: '禁用'
  }
  return map[status || ''] || status || '-'
}

const loadDevices = async () => {
  try {
    const res = await axios.get('/api/v1/devices', { params: { page_size: 100 } })
    devices.value = res.data.data?.items || []
  } catch (error) {
    console.error('Failed to load devices:', error)
  }
}

const loadDataSources = async () => {
  loading.value = true
  try {
    const params: any = {
      page: currentPage.value,
      page_size: pageSize.value
    }
    if (filterDeviceId.value) params.device_id = filterDeviceId.value
    if (filterStatus.value) params.status = filterStatus.value

    const res = await axios.get('/api/v1/data-sources', { params })
    dataSources.value = res.data.data?.items || []
    total.value = res.data.data?.total || 0
  } catch (error) {
    ElMessage.error('加载数据源列表失败')
  } finally {
    loading.value = false
  }
}

const showDetail = (row: DataSource) => {
  currentSource.value = row
  detailVisible.value = true
}

const handleDisable = async (row: DataSource) => {
  try {
    await ElMessageBox.confirm('确定要禁用该数据源吗？', '提示', { type: 'warning' })
    await axios.put(`/api/v1/data-sources/${row.id}`, { status: 'disabled' })
    ElMessage.success('禁用成功')
    loadDataSources()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error('操作失败')
    }
  }
}

onMounted(() => {
  loadDevices()
  loadDataSources()
})
</script>

<style scoped>
.data-source-manage {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.filter-bar {
  display: flex;
  gap: 10px;
  margin-bottom: 20px;
}

.pagination {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}
</style>
