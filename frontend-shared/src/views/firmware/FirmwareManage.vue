<template>
  <div class="firmware-manage">
    <PageHeader title="固件管理">
      <template #extra>
        <el-button type="primary" :icon="Upload" @click="showUploadDialog = true">
          上传固件
        </el-button>
      </template>
    </PageHeader>

    <el-card>
      <el-skeleton v-if="loading" :rows="5" animated />
      <template v-else>
        <!-- 批量操作栏 -->
        <div v-if="selectedFirmwares.length > 0" class="batch-bar">
          <span class="batch-info">
            已选择 <strong>{{ selectedFirmwares.length }}</strong> 项
          </span>
          <el-button type="danger" size="small" :icon="Delete" :loading="batchDeleting" @click="handleBatchDelete">
            批量删除
          </el-button>
          <el-button size="small" @click="selectedFirmwares = []">取消选择</el-button>
        </div>

        <el-table :data="firmwares" stripe @selection-change="handleSelectionChange" ref="tableRef">
          <el-table-column type="selection" width="45" />
          <el-table-column prop="id" label="ID" width="50" />
          <el-table-column label="版本号" width="120">
            <template #default="{ row }">
              <el-tag :type="row.stable ? 'success' : 'info'" size="small">
                {{ formatVersion(row.version) }}
              </el-tag>
              <el-icon v-if="row.stable" class="stable-icon" title="稳定版本"><CircleCheckFilled /></el-icon>
            </template>
          </el-table-column>
          <el-table-column label="目标型号" width="120">
            <template #default="{ row }">
              <span v-if="row.target_model">{{ row.target_model }}</span>
              <span v-else class="text-muted">通用</span>
            </template>
          </el-table-column>
          <el-table-column prop="size_bytes" label="文件大小" width="100">
            <template #default="{ row }">
              <span>{{ formatFileSize(row.size_bytes) }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="checksum" label="SHA256" width="180" show-overflow-tooltip>
            <template #default="{ row }">
              <code class="checksum-text">{{ row.checksum?.substring(0, 16) }}...</code>
            </template>
          </el-table-column>
          <el-table-column prop="created_at" label="创建时间" width="170">
            <template #default="{ row }">
              <span>{{ formatTime(row.created_at) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="320" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" size="small" @click="handleEdit(row)">
                <el-icon><Edit /></el-icon> 编辑
              </el-button>
              <el-button size="small" @click="handleCopyUrl(row)">
                <el-icon><CopyDocument /></el-icon> 复制链接
              </el-button>
              <el-button size="small" @click="handleDownload(row)">
                <el-icon><Download /></el-icon> 下载
              </el-button>
              <el-button type="danger" size="small" @click="handleDelete(row)">
                <el-icon><Delete /></el-icon>
              </el-button>
            </template>
          </el-table-column>
        </el-table>

        <div style="display: flex; justify-content: center; margin-top: 16px;" v-if="total > 0">
          <el-pagination
            v-model:current-page="currentPage"
            v-model:page-size="pageSize"
            :page-sizes="[10, 20, 50]"
            :total="total"
            layout="total, sizes, prev, pager, next, jumper"
            @size-change="() => fetchFirmwares()"
            @current-change="fetchFirmwares"
          />
        </div>
        <el-empty v-if="firmwares.length === 0" description="暂无固件数据">
          <el-button type="primary" @click="showUploadDialog = true">上传固件</el-button>
        </el-empty>
      </template>
    </el-card>

    <!-- 上传固件对话框 -->
    <el-dialog v-model="showUploadDialog" title="上传固件" align-center width="500px" @close="resetUploadForm">
      <el-form :model="uploadForm" :rules="uploadRules" label-width="100px">
        <el-form-item label="版本号" prop="version">
          <el-input v-model="uploadForm.version" placeholder="如: 1.0.0" />
        </el-form-item>
        <el-form-item label="目标型号" prop="target_model">
          <el-input v-model="uploadForm.target_model" placeholder="留空表示通用" />
        </el-form-item>
        <el-form-item label="固件文件" prop="file">
          <el-upload
            ref="uploadRef"
            :auto-upload="false"
            :limit="1"
            :on-change="handleFileChange"
            :on-exceed="handleExceed"
            accept=".bin"
          >
            <el-button>选择文件</el-button>
          </el-upload>
          <div class="upload-tip">支持 .bin 格式文件</div>
          <div v-if="uploadForm.file" class="file-info">
            已选择: {{ uploadForm.file.name }} ({{ formatFileSize(uploadForm.file.size) }})
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showUploadDialog = false">取消</el-button>
        <el-button type="primary" :loading="uploading" @click="handleUpload">上传</el-button>
      </template>
    </el-dialog>

    <!-- 编辑固件对话框 -->
    <el-dialog v-model="showEditDialog" title="编辑固件" align-center width="560px" :close-on-click-modal="false">
      <el-form :model="editForm" label-width="100px">
        <el-form-item label="版本号">
          <el-input v-model="editForm.version" placeholder="如: 1.0.0" />
        </el-form-item>
        <el-form-item label="目标型号">
          <el-input v-model="editForm.target_model" placeholder="留空表示通用" />
        </el-form-item>
        <el-form-item label="更新日志">
          <el-input v-model="editForm.changelog" type="textarea" :rows="4" placeholder="本次固件更新的内容描述" />
        </el-form-item>
        <el-form-item label="稳定版本">
          <el-switch v-model="editForm.stable" />
          <span class="form-hint">标记为稳定版本后，OTA 升级时优先推荐</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEditDialog = false">取消</el-button>
        <el-button type="primary" :loading="editing" @click="handleEditSubmit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Upload, Edit, Download, Delete, CopyDocument, CircleCheckFilled } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type UploadInstance, type UploadProps } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
import { firmwareApi, type Firmware } from '@/api/firmware'

const firmwares = ref<Firmware[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)
const showUploadDialog = ref(false)
const uploading = ref(false)
const uploadRef = ref<UploadInstance>()

// 批量选择
const selectedFirmwares = ref<Firmware[]>([])
const batchDeleting = ref(false)

// 编辑相关
const showEditDialog = ref(false)
const editing = ref(false)
const editingFirmwareId = ref<number | null>(null)

const editForm = reactive({
  version: '',
  target_model: '',
  changelog: '',
  stable: false
})

const uploadForm = reactive({
  version: '',
  target_model: '',
  file: null as File | null
})

const uploadRules = {
  version: [{ required: true, message: '请输入版本号', trigger: 'blur' }],
  file: [{ required: true, message: '请选择固件文件', trigger: 'change' }]
}

const fetchFirmwares = async () => {
  loading.value = true
  try {
    const response = await firmwareApi.getList({
      page: currentPage.value,
      page_size: pageSize.value
    })
    firmwares.value = response.list || []
    total.value = response.total || 0
  } catch {
    ElMessage.error('获取固件列表失败')
  } finally {
    loading.value = false
  }
}

// 批量选择
const handleSelectionChange = (rows: Firmware[]) => {
  selectedFirmwares.value = rows
}

const handleBatchDelete = async () => {
  const count = selectedFirmwares.value.length
  try {
    await ElMessageBox.confirm(
      `确定要删除选中的 ${count} 个固件吗？此操作不可恢复。`,
      '批量删除',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }
    )

    batchDeleting.value = true
    const ids = selectedFirmwares.value.map(f => f.id)
    await Promise.all(ids.map(id => firmwareApi.delete(id)))
    ElMessage.success(`已删除 ${count} 个固件`)
    selectedFirmwares.value = []
    await fetchFirmwares()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('批量删除失败: ' + (error.message || '未知错误'))
    }
  } finally {
    batchDeleting.value = false
  }
}

// 单个删除
const handleDelete = async (row: Firmware) => {
  try {
    await ElMessageBox.confirm(
      `确定要删除固件版本 "${formatVersion(row.version)}" 吗？`,
      '提示',
      { confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning' }
    )

    await firmwareApi.delete(row.id)
    ElMessage.success('删除成功')
    await fetchFirmwares()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('删除失败')
    }
  }
}

// 编辑固件
const handleEdit = (row: Firmware) => {
  editingFirmwareId.value = row.id
  editForm.version = row.version?.replace(/^v/i, '') || ''
  editForm.target_model = row.target_model || ''
  editForm.changelog = row.changelog || ''
  editForm.stable = row.stable || false
  showEditDialog.value = true
}

const handleEditSubmit = async () => {
  if (!editingFirmwareId.value) return
  editing.value = true
  try {
    await firmwareApi.update(editingFirmwareId.value, {
      version: editForm.version,
      target_model: editForm.target_model,
      changelog: editForm.changelog,
      stable: editForm.stable
    })
    ElMessage.success('更新成功')
    showEditDialog.value = false
    await fetchFirmwares()
  } catch (error: any) {
    ElMessage.error(error.message || '更新失败')
  } finally {
    editing.value = false
  }
}

// 复制下载链接
const handleCopyUrl = async (row: Firmware) => {
  const url = firmwareApi.getDownloadUrl(row.filename)
  try {
    await navigator.clipboard.writeText(url)
    ElMessage.success('下载链接已复制到剪贴板')
  } catch {
    // Fallback for non-HTTPS or older browsers
    const textarea = document.createElement('textarea')
    textarea.value = url
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    try {
      document.execCommand('copy')
      ElMessage.success('下载链接已复制到剪贴板')
    } catch {
      ElMessage.error('复制失败，请手动复制: ' + url)
    }
    document.body.removeChild(textarea)
  }
}

// 下载固件
const handleDownload = (row: Firmware) => {
  const url = firmwareApi.getDownloadUrl(row.filename)
  const link = document.createElement('a')
  link.href = url
  link.download = `firmware_${formatVersion(row.version)}.bin`
  link.click()
  ElMessage.success('开始下载')
}

// 版本号格式化：统一加 v 前缀
const formatVersion = (version: string) => {
  if (!version) return 'v-'
  if (version.startsWith('v') || version.startsWith('V')) return version
  return `v${version}`
}

const handleFileChange: UploadProps['onChange'] = (uploadFile) => {
  if (uploadFile.raw) {
    uploadForm.file = uploadFile.raw
  }
}

const handleExceed: UploadProps['onExceed'] = () => {
  ElMessage.warning('只能上传一个固件文件')
}

const handleUpload = async () => {
  if (!uploadForm.file) {
    ElMessage.warning('请先选择固件文件')
    return
  }

  uploading.value = true
  try {
    const formData = new FormData()
    formData.append('version', uploadForm.version)
    if (uploadForm.target_model) {
      formData.append('target_model', uploadForm.target_model)
    }
    formData.append('file', uploadForm.file)

    await firmwareApi.upload(formData)
    ElMessage.success('上传成功')
    showUploadDialog.value = false
    resetUploadForm()
    await fetchFirmwares()
  } catch (error: any) {
    ElMessage.error(error.message || '上传失败')
  } finally {
    uploading.value = false
  }
}

const resetUploadForm = () => {
  uploadForm.version = ''
  uploadForm.target_model = ''
  uploadForm.file = null
  uploadRef.value?.clearFiles()
}

const formatFileSize = (bytes: number | undefined | null) => {
  if (!bytes && bytes !== 0) return '-'
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}

onMounted(() => {
  fetchFirmwares()
})
</script>

<style scoped>
.firmware-manage {
  padding: 0;
}

.batch-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 16px;
  margin-bottom: 12px;
  background: var(--el-color-danger-light-9);
  border-radius: 4px;
}

.batch-info {
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.stable-icon {
  color: var(--el-color-success);
  margin-left: 4px;
  vertical-align: middle;
}

.checksum-text {
  font-family: 'Courier New', monospace;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.text-muted {
  color: var(--el-text-color-placeholder);
}

.file-info {
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.upload-tip {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.form-hint {
  margin-left: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
