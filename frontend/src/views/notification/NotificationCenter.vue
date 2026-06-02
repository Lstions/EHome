<template>
  <div class="notification-center">
    <div class="page-header">
      <h2>通知中心</h2>
      <div class="header-actions">
        <el-badge :value="unreadCount" :max="99" :hidden="unreadCount === 0">
          <el-button @click="loadNotifications">
            <el-icon><Bell /></el-icon>
            刷新
          </el-button>
        </el-badge>
        <el-button @click="handleMarkAllRead" :disabled="unreadCount === 0">
          全部已读
        </el-button>
      </div>
    </div>

    <el-card>
      <div class="filter-bar">
        <el-select v-model="filterType" placeholder="类型" clearable @change="loadNotifications">
          <el-option label="系统" value="system" />
          <el-option label="设备" value="device" />
          <el-option label="告警" value="alert" />
          <el-option label="OTA" value="ota" />
        </el-select>
        <el-select v-model="filterRead" placeholder="已读状态" clearable @change="loadNotifications">
          <el-option label="未读" :value="false" />
          <el-option label="已读" :value="true" />
        </el-select>
      </div>

      <div class="notification-list" v-loading="loading">
        <div
          v-for="item in notifications"
          :key="item.id"
          class="notification-item"
          :class="{ unread: !item.is_read }"
          @click="handleRead(item)"
        >
          <div class="notification-icon">
            <el-icon :size="24" :color="getTypeColor(item.type)">
              <component :is="getTypeIcon(item.type)" />
            </el-icon>
          </div>
          <div class="notification-content">
            <div class="notification-title">{{ item.title }}</div>
            <div class="notification-message">{{ item.message }}</div>
            <div class="notification-time">{{ formatTime(item.created_at) }}</div>
          </div>
          <div class="notification-status">
            <el-tag v-if="!item.is_read" type="danger" size="small">未读</el-tag>
          </div>
        </div>

        <el-empty v-if="notifications.length === 0" description="暂无通知" />
      </div>

      <div class="pagination">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @size-change="loadNotifications"
          @current-change="loadNotifications"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Bell, Warning, Setting, Download, InfoFilled } from '@element-plus/icons-vue'
import axios from 'axios'

interface Notification {
  id: number
  type: string
  title: string
  message: string
  is_read: boolean
  created_at: string
}

const notifications = ref<Notification[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)
const unreadCount = ref(0)

const filterType = ref('')
const filterRead = ref<boolean | undefined>(undefined)

const getTypeColor = (type?: string) => {
  const map: Record<string, string> = {
    system: '#409EFF',
    device: '#67C23A',
    alert: '#F56C6C',
    ota: '#E6A23C'
  }
  return map[type || ''] || '#909399'
}

const getTypeIcon = (type?: string) => {
  const map: Record<string, any> = {
    system: Setting,
    device: InfoFilled,
    alert: Warning,
    ota: Download
  }
  return map[type || ''] || InfoFilled
}

const formatTime = (time?: string) => {
  if (!time) return '-'
  return new Date(time).toLocaleString()
}

const loadNotifications = async () => {
  loading.value = true
  try {
    const params: any = {
      page: currentPage.value,
      page_size: pageSize.value
    }
    if (filterType.value) params.type = filterType.value
    if (filterRead.value !== undefined) params.is_read = filterRead.value

    const res = await axios.get('/api/v1/notifications', { params })
    notifications.value = res.data.data || []
    total.value = res.data.total || 0
  } catch (error) {
    ElMessage.error('加载通知列表失败')
  } finally {
    loading.value = false
  }
}

const loadUnreadCount = async () => {
  try {
    const res = await axios.get('/api/v1/notifications/unread-count')
    unreadCount.value = res.data.data?.count || 0
  } catch (error) {
    console.error('Failed to load unread count:', error)
  }
}

const handleRead = async (item: Notification) => {
  if (item.is_read) return
  try {
    await axios.put(`/api/v1/notifications/${item.id}/read`)
    item.is_read = true
    unreadCount.value = Math.max(0, unreadCount.value - 1)
  } catch (error) {
    ElMessage.error('标记已读失败')
  }
}

const handleMarkAllRead = async () => {
  try {
    await axios.put('/api/v1/notifications/read-all')
    notifications.value.forEach(item => item.is_read = true)
    unreadCount.value = 0
    ElMessage.success('全部已读')
  } catch (error) {
    ElMessage.error('操作失败')
  }
}

onMounted(() => {
  loadNotifications()
  loadUnreadCount()
})
</script>

<style scoped>
.notification-center {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header-actions {
  display: flex;
  gap: 10px;
}

.filter-bar {
  display: flex;
  gap: 10px;
  margin-bottom: 20px;
}

.notification-list {
  min-height: 200px;
}

.notification-item {
  display: flex;
  align-items: flex-start;
  padding: 15px;
  border-bottom: 1px solid #eee;
  cursor: pointer;
  transition: background-color 0.2s;
}

.notification-item:hover {
  background-color: #f5f7fa;
}

.notification-item.unread {
  background-color: #fef0f0;
}

.notification-icon {
  margin-right: 15px;
  flex-shrink: 0;
}

.notification-content {
  flex: 1;
}

.notification-title {
  font-weight: bold;
  margin-bottom: 5px;
}

.notification-message {
  color: #666;
  font-size: 14px;
  margin-bottom: 5px;
}

.notification-time {
  color: #999;
  font-size: 12px;
}

.notification-status {
  flex-shrink: 0;
  margin-left: 10px;
}

.pagination {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}
</style>
