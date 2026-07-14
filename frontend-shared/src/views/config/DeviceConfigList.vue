<template>
  <div class="config-page">
    <!-- 顶部统计 -->
    <div class="stats-row">
      <div class="stat-card">
        <div class="stat-icon">
          <el-icon><Document /></el-icon>
        </div>
        <div class="stat-content">
          <span class="stat-value">{{ stats.total }}</span>
          <span class="stat-label">本页模板</span>
        </div>
      </div>
      
      <div class="stat-card">
        <div class="stat-icon active">
          <el-icon><CircleCheck /></el-icon>
        </div>
        <div class="stat-content">
          <span class="stat-value">{{ stats.active }}</span>
          <span class="stat-label">本页启用</span>
        </div>
      </div>
      
      <div class="stat-card">
        <div class="stat-icon bus">
          <el-icon><Connection /></el-icon>
        </div>
        <div class="stat-content">
          <span class="stat-value">{{ stats.busTypes }}</span>
          <span class="stat-label">总线类型</span>
        </div>
      </div>
      
      <div class="stat-card">
        <div class="stat-icon device">
          <el-icon><Cpu /></el-icon>
        </div>
        <div class="stat-content">
          <span class="stat-value">{{ stats.deviceTypes }}</span>
          <span class="stat-label">设备类型</span>
        </div>
      </div>
    </div>

    <!-- 工具栏 -->
    <el-card class="toolbar-card" shadow="hover">
      <div class="filter-bar">
        <div class="filter-left">
          <el-input
            v-model="searchKeyword"
            placeholder="搜索模板名称..."
            prefix-icon="Search"
            clearable
            class="search-input"
            @input="handleSearch"
          />
          
          <el-select v-model="typeFilter" placeholder="设备类型" clearable>
            <template #prefix>
              <el-icon><Grid /></el-icon>
            </template>
            <el-option v-for="t in deviceTypeOptions" :key="t.value" :label="t.label" :value="t.value" />
          </el-select>
          
          <el-select v-model="hardwareFilter" placeholder="硬件类型" clearable>
            <el-option label="UART" value="uart" />
            <el-option label="I2C" value="i2c" />
            <el-option label="SPI" value="spi" />
            <el-option label="ADC" value="adc" />
          </el-select>
          
          <el-select v-model="statusFilter" placeholder="状态" clearable>
            <el-option label="启用" value="active" />
            <el-option label="禁用" value="inactive" />
          </el-select>
        </div>
        
        <div class="filter-right">
          <el-button @click="importConfig">
            <el-icon><Upload /></el-icon>
            导入
          </el-button>
          <el-button @click="exportConfigs" :disabled="!filteredConfigs || filteredConfigs.length === 0">
            <el-icon><Download /></el-icon>
            导出
          </el-button>
          <el-button type="primary" @click="showFormDialog = true">
            <el-icon><Plus /></el-icon>
            新建模板
          </el-button>
        </div>
      </div>
    </el-card>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''; handleFiltersChanged()">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="typeFilter" closable @close="typeFilter = ''; handleFiltersChanged()">类型：{{ getDeviceTypeLabel(typeFilter) }}</el-tag>
      <el-tag v-if="hardwareFilter" closable @close="hardwareFilter = ''; handleFiltersChanged()">总线：{{ hardwareFilter.toUpperCase() }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''; handleFiltersChanged()">状态：{{ statusFilter === 'active' ? '启用' : '禁用' }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 配置模板卡片 -->
    <div class="config-grid">
      <el-card 
        v-for="config in filteredConfigs" 
        :key="config.id" 
        class="config-card"
        :class="{ inactive: config.status === 'inactive' }"
        shadow="hover"
      >
        <div class="card-header">
          <div class="config-icon" :class="config.hardware_type">
            <el-icon :size="24"><component :is="getBusIcon(config.hardware_type)" /></el-icon>
          </div>
          <div class="config-info">
            <h3>{{ config.name }}</h3>
            <p class="desc">{{ config.description || '暂无描述' }}</p>
          </div>
          <div class="config-badges">
            <el-tag v-if="config.is_default" type="success" size="small">默认</el-tag>
            <el-tag :type="config.status === 'active' ? 'success' : 'info'" size="small">
              {{ config.status === 'active' ? '启用' : '禁用' }}
            </el-tag>
          </div>
        </div>
        
        <div class="card-body">
          <div class="spec-list">
            <div class="spec-item">
              <span class="label">设备类型</span>
              <el-tag size="small">{{ getDeviceTypeLabel(config.device_type) }}</el-tag>
            </div>
            
            <div class="spec-item">
              <span class="label">通信协议</span>
              <el-tag :type="config.protocol === 'modbus' ? 'primary' : 'info'" size="small">
                {{ config.protocol?.toUpperCase() }}
              </el-tag>
            </div>
            
            <div class="spec-item">
              <span class="label">总线类型</span>
              <el-tag type="warning" size="small">{{ config.hardware_type?.toUpperCase() }}</el-tag>
            </div>
            
            <div class="spec-item" v-if="config.config">
              <span class="label">参数配置</span>
              <div class="params-preview">
                <el-tag 
                  v-for="(v, k) in getParamsPreview(config.config)" 
                  :key="k" 
                  size="small" 
                  type="info"
                >
                  {{ k }}: {{ v }}
                </el-tag>
              </div>
            </div>
          </div>
        </div>
        
        <div class="card-footer">
          <el-button size="small" @click="handlePreview(config)">
            <el-icon><View /></el-icon>
            预览
          </el-button>
          <el-button size="small" @click="handleClone(config)">
            <el-icon><CopyDocument /></el-icon>
            克隆
          </el-button>
          <el-button size="small" @click="handleEdit(config)">
            <el-icon><Edit /></el-icon>
            编辑
          </el-button>
          <el-dropdown @command="(cmd: string) => handleMoreAction(cmd, config)">
            <el-button size="small">
              <el-icon><MoreFilled /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="setDefault" :disabled="config.is_default">
                  设为默认
                </el-dropdown-item>
                <el-dropdown-item command="toggle">
                  {{ config.status === 'active' ? '禁用' : '启用' }}
                </el-dropdown-item>
                <el-dropdown-item command="export">导出配置</el-dropdown-item>
                <el-dropdown-item command="delete" divided>
                  <span style="color: var(--el-color-danger);">删除</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-card>
    </div>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && filteredConfigs.length === 0"
      :kind="hasActiveFilters ? 'filtered' : 'initial'"
      icon="Document"
      :title="hasActiveFilters ? '没有匹配的配置模板' : '暂无配置模板'"
      :description="hasActiveFilters ? '请调整筛选条件，或清除全部筛选后重试。' : '创建模板后，可为边缘设备复用连接、解析与初始化配置。'"
      :quick-actions="hasActiveFilters
        ? [{ label: '清除全部筛选', type: 'primary', handler: clearFilters }]
        : [{ label: '新建模板', type: 'primary', handler: () => { showFormDialog = true } }]"
    />

    <!-- 分页 -->
    <div v-if="total > 0" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[12, 24, 48]"
        layout="total, sizes, prev, pager, next"
        @current-change="fetchConfigs"
        @size-change="fetchConfigs"
      />
    </div>

    <!-- 预览对话框 -->
    <el-dialog v-model="previewVisible" title="配置预览" width="640px">
      <el-descriptions v-if="previewConfig" :column="2" border>
        <el-descriptions-item label="模板名称">{{ previewConfig.name }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag :type="previewConfig.status === 'active' ? 'success' : 'info'">
            {{ previewConfig.status === 'active' ? '启用' : '禁用' }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="设备类型">{{ getDeviceTypeLabel(previewConfig.device_type) }}</el-descriptions-item>
        <el-descriptions-item label="硬件类型">{{ previewConfig.hardware_type?.toUpperCase() }}</el-descriptions-item>
        <el-descriptions-item label="通信协议">{{ previewConfig.protocol?.toUpperCase() }}</el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ formatTime(previewConfig.created_at) }}</el-descriptions-item>
        <el-descriptions-item label="描述" :span="2">{{ previewConfig.description || '无' }}</el-descriptions-item>
      </el-descriptions>
      
      <div v-if="previewConfig?.config" class="config-json">
        <h4>配置参数 (JSON)</h4>
        <pre>{{ JSON.stringify(previewConfig.config, null, 2) }}</pre>
      </div>
    </el-dialog>

    <!-- 创建/编辑对话框 -->
    <DeviceConfigForm
      v-model:visible="showFormDialog"
      :config="editingConfig"
      @success="handleFormSuccess"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { 
  Document, CircleCheck, Connection, Cpu, Search, Grid, 
  Upload, Download, Plus, View, Edit, CopyDocument, MoreFilled,
  DataBoard, DataAnalysis, Files, Cpu as CpuIcon
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import DeviceConfigForm from '@/components/forms/DeviceConfigForm.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import { deviceTypeOptions } from '@/utils/deviceType'

// 状态
const loading = ref(false)
const configs = ref<DeviceConfig[]>([])
const currentPage = ref(1)
const pageSize = ref(12)
const total = ref(0)
const searchKeyword = ref('')
const typeFilter = ref('')
const hardwareFilter = ref('')
const statusFilter = ref('')
const hasActiveFilters = computed(() => Boolean(searchKeyword.value || typeFilter.value || hardwareFilter.value || statusFilter.value))

// 对话框
const showFormDialog = ref(false)
const previewVisible = ref(false)
const editingConfig = ref<DeviceConfig | null>(null)
const previewConfig = ref<DeviceConfig | null>(null)

// 统计数据
const stats = reactive({
  total: 0,
  active: 0,
  busTypes: 0,
  deviceTypes: 0
})

// 设备类型选项 — 从 deviceType.ts 统一导入

// 过滤后的配置
const filteredConfigs = computed(() => {
  let result = configs.value
  
  if (searchKeyword.value) {
    const kw = searchKeyword.value.toLowerCase()
    result = result.filter(c => c.name?.toLowerCase().includes(kw))
  }
  
  if (typeFilter.value) {
    result = result.filter(c => c.device_type === typeFilter.value)
  }
  
  if (hardwareFilter.value) {
    result = result.filter(c => c.hardware_type === hardwareFilter.value)
  }
  
  if (statusFilter.value) {
    result = result.filter(c => c.status === statusFilter.value)
  }
  
  return result
})

// 获取配置列表
const fetchConfigs = async () => {
  loading.value = true
  try {
    const response = await deviceConfigApi.getList({
      device_type: typeFilter.value || undefined,
      hardware_type: hardwareFilter.value || undefined,
      page: currentPage.value,
      page_size: pageSize.value
    })
    
    configs.value = response.list || []
    total.value = response.total || 0
    updateStats()
  } catch (error: any) {
    ElMessage.error('获取配置模板列表失败')
  } finally {
    loading.value = false
  }
}

// 更新统计
const updateStats = () => {
  stats.total = configs.value.length
  stats.active = configs.value.filter(c => c.status === 'active').length
  
  const hardwareTypes = new Set(configs.value.map(c => c.hardware_type))
  stats.busTypes = hardwareTypes.size
  
  const deviceTypes = new Set(configs.value.map(c => c.device_type))
  stats.deviceTypes = deviceTypes.size
}

// 搜索
const handleSearch = () => {
  handleFiltersChanged()
}

const handleFiltersChanged = () => {
  currentPage.value = 1
  fetchConfigs()
}

const clearFilters = () => {
  searchKeyword.value = ''
  typeFilter.value = ''
  hardwareFilter.value = ''
  statusFilter.value = ''
  currentPage.value = 1
  fetchConfigs()
}

watch([typeFilter, hardwareFilter, statusFilter], handleFiltersChanged)

// 总线图标
const getBusIcon = (busType: string) => {
  const iconMap: Record<string, any> = {
    uart: DataBoard,
    i2c: DataAnalysis,
    spi: Files,
    adc: Files,
  }
  return iconMap[busType] || Files
}

// 设备类型标签
const getDeviceTypeLabel = (type: string) => {
  return deviceTypeOptions.find(t => t.value === type)?.label || type
}

// 参数预览
const getParamsPreview = (config: any) => {
  if (!config) return {}
  
  const preview: Record<string, any> = {}
  const keys = Object.keys(config).slice(0, 3)
  
  for (const key of keys) {
    let value = config[key]
    if (typeof value === 'number') {
      value = value.toString()
    }
    if (value && typeof value === 'string' && value.length > 10) {
      value = value.slice(0, 10) + '...'
    }
    preview[key] = value
  }
  
  return preview
}

// 预览
const handlePreview = (config: DeviceConfig) => {
  previewConfig.value = config
  previewVisible.value = true
}

// 编辑
const handleEdit = (config: DeviceConfig) => {
  editingConfig.value = { ...config }
  showFormDialog.value = true
}

// 克隆
const handleClone = async (config: DeviceConfig) => {
  try {
    await ElMessageBox.confirm(
      `确定要克隆配置 "${config.name}" 吗？`,
      '克隆配置',
      { confirmButtonText: '确定', cancelButtonText: '取消' }
    )
    
    const clonedConfig = {
      ...config,
      id: undefined,
      name: `${config.name} (副本)`,
      is_default: false,
      created_at: undefined,
      updated_at: undefined
    }
    
    await deviceConfigApi.create(clonedConfig)
    ElMessage.success('克隆成功')
    await fetchConfigs()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('克隆失败')
    }
  }
}

// 更多操作
const handleMoreAction = async (command: string, config: DeviceConfig) => {
  switch (command) {
    case 'setDefault':
      try {
        await deviceConfigApi.setDefault(config.id)
        ElMessage.success('设置成功')
        await fetchConfigs()
      } catch (error) {
        ElMessage.error('设置失败')
      }
      break
      
    case 'toggle':
      try {
        const newStatus = config.status === 'active' ? 'inactive' : 'active'
        await deviceConfigApi.update(config.id, { status: newStatus })
        ElMessage.success(newStatus === 'active' ? '已启用' : '已禁用')
        await fetchConfigs()
      } catch (error) {
        ElMessage.error('操作失败')
      }
      break
      
    case 'export':
      exportConfig(config)
      break
      
    case 'delete':
      try {
        await ElMessageBox.confirm(
          `确定要删除配置 "${config.name}" 吗？`,
          '警告',
          { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }
        )
        
        await deviceConfigApi.delete(config.id)
        ElMessage.success('删除成功')
        await fetchConfigs()
      } catch (error: any) {
        if (error !== 'cancel') {
          ElMessage.error('删除失败')
        }
      }
      break
  }
}

// 导出单个配置
const exportConfig = (config: DeviceConfig) => {
  const data = JSON.stringify(config, null, 2)
  const blob = new Blob([data], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${config.name}.json`
  link.click()
  URL.revokeObjectURL(url)
  
  ElMessage.success('导出成功')
}

// 导入
const importConfig = () => {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.json'
  input.onchange = async (e) => {
    const file = (e.target as HTMLInputElement).files?.[0]
    if (!file) return
    
    try {
      const text = await file.text()
      const config = JSON.parse(text)
      
      // 验证必要字段
      if (!config.name || !config.device_type || !config.hardware_type) {
        throw new Error('配置文件格式不正确')
      }
      
      delete config.id
      delete config.created_at
      delete config.updated_at
      config.is_default = false
      
      await deviceConfigApi.create(config)
      ElMessage.success('导入成功')
      await fetchConfigs()
    } catch (error: any) {
      ElMessage.error(error.message || '导入失败')
    }
  }
  input.click()
}

// 批量导出
const exportConfigs = () => {
  if (filteredConfigs.value.length === 0) {
    ElMessage.warning('暂无配置可导出')
    return
  }
  
  const data = JSON.stringify(filteredConfigs.value, null, 2)
  const blob = new Blob([data], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `device_configs_${Date.now()}.json`
  link.click()
  URL.revokeObjectURL(url)
  
  ElMessage.success('导出成功')
}

// 表单成功
const handleFormSuccess = () => {
  showFormDialog.value = false
  editingConfig.value = null
  fetchConfigs()
}

// 格式化时间
const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}

onMounted(() => {
  fetchConfigs()
})
</script>

<style scoped>
.config-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

/* 统计 */
.stats-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}

.stat-card {
  background: #fff;
  border-radius: 12px;
  padding: 20px;
  display: flex;
  align-items: center;
  gap: 16px;
  border: 1px solid var(--el-border-color);
}

.stat-icon {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 20px;
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
}

.stat-icon.active { background: var(--el-color-success); }
.stat-icon.bus { background: var(--el-color-warning); }
.stat-icon.device { background: var(--el-text-color-secondary); }

.stat-content { flex: 1; }
.stat-value { display: block; font-size: 24px; font-weight: 600; color: var(--el-text-color-primary); }
.stat-label { font-size: 13px; color: var(--el-text-color-secondary); }

/* 工具栏 */
.toolbar-card :deep(.el-card__body) {
  padding: 16px 20px;
}

.filter-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
}

.filter-left {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.search-input {
  width: 200px;
}

.filter-right {
  display: flex;
  gap: 8px;
}

/* 配置网格 */
.config-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 16px;
}

.config-card {
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.config-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
}

.config-card.inactive {
  opacity: 0.7;
}

.card-header {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
}

.config-icon {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
}

.config-icon.uart { background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%); }
.config-icon.i2c { background: linear-gradient(135deg, var(--el-color-warning) 0%, var(--el-color-danger) 100%); }
.config-icon.spi { background: linear-gradient(135deg, var(--el-text-color-secondary) 0%, var(--el-text-color-placeholder) 100%); }
.config-icon.gpio { background: linear-gradient(135deg, var(--el-color-success) 0%, var(--el-color-success-light-3) 100%); }
.config-icon.adc { background: linear-gradient(135deg, #9c27b0 0%, #ba68c8 100%); }

.config-info {
  flex: 1;
  min-width: 0;
}

.config-info h3 {
  margin: 0;
  font-size: 16px;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.config-info .desc {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.config-badges {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.card-body {
  padding: 16px 0;
  border-top: 1px solid var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-fill-color-light);
}

.spec-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.spec-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.spec-item .label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.params-preview {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 200px;
  justify-content: flex-end;
}

.card-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 12px;
}

/* 分页 */
.pagination-wrapper {
  display: flex;
  justify-content: center;
  padding: 20px 0;
}

.active-filters {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.active-filters-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

/* 预览对话框 */
.config-json {
  margin-top: 20px;
}

.config-json h4 {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.config-json pre {
  margin: 0;
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 6px;
  font-size: 12px;
  font-family: monospace;
  max-height: 300px;
  overflow: auto;
}

/* 响应式 */
@media (max-width: 1200px) {
  .stats-row { grid-template-columns: repeat(2, 1fr); }
}

@media (max-width: 768px) {
  .stats-row { grid-template-columns: 1fr; }
  .filter-left, .filter-right { width: 100%; }
  .config-grid { grid-template-columns: 1fr; }
}
</style>
