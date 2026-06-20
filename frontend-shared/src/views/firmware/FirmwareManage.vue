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
      <el-table v-else :data="firmwares" stripe>
        <el-table-column prop="id" label="ID" width="50" />
        <el-table-column prop="version" label="版本号">
          <template #default="{ row }">
            <span>{{ formatVersion(row.version) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="checksum" label="校验和(SHA256)" width="240" show-overflow-tooltip />
        <el-table-column prop="size_bytes" label="文件大小" width="100">
          <template #default="{ row }">
            <span>{{ formatFileSize(row.size_bytes) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="url" label="下载地址" width="250" show-overflow-tooltip />
        <el-table-column prop="created_at" label="创建时间" width="170">
          <template #default="{ row }">
            <span>{{ formatTime(row.created_at) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button
              type="primary"
              size="small"
              @click="handleEdit(row)"
            >
              <el-icon><Edit /></el-icon>
              编辑
            </el-button>
            <el-button
              size="small"
              @click="handleDownload(row)"
            >
              <el-icon><Download /></el-icon>
              下载
            </el-button>
            <el-button
              type="danger"
              size="small"
              @click="handleDelete(row)"
            >
              <el-icon><Delete /></el-icon>
              删除
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
          @size-change="fetchFirmwares"
          @current-change="fetchFirmwares"
        />
      </div>
      <el-empty v-if="!loading && firmwares.length === 0" description="暂无固件数据">
        <el-button type="primary" @click="showUploadDialog = true">上传固件</el-button>
      </el-empty>
    </el-card>

    <!-- 上传固件对话框 -->
    <el-dialog
      v-model="showUploadDialog"
      title="上传固件"
      align-center
      width="500px"
    >
      <el-form :model="uploadForm" :rules="uploadRules" label-width="100px">
        <el-form-item label="版本号" prop="version">
          <el-input v-model="uploadForm.version" placeholder="如: 1.0.0" />
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
            已选择: {{ uploadForm.file.name }}
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showUploadDialog = false">取消</el-button>
        <el-button type="primary" :loading="uploading" @click="handleUpload">
          上传
        </el-button>
      </template>
    </el-dialog>

    <!-- 编辑固件对话框 -->
    <el-dialog
      v-model="showEditDialog"
      title="编辑固件"
      align-center
      width="500px"
      :close-on-click-modal="false"
    >
      <el-form :model="editForm" label-width="100px">
        <el-form-item label="版本号">
          <el-input v-model="editForm.version" placeholder="如: 1.0.0" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEditDialog = false">取消</el-button>
        <el-button type="primary" :loading="editing" @click="handleEditSubmit">
          保存
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { Upload, Edit, Download, Delete } from '@element-plus/icons-vue'
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

// 编辑相关
const showEditDialog = ref(false)
const editing = ref(false)
const editingFirmwareId = ref<number | null>(null)

const editForm = reactive({
  version: ''
})

const uploadForm = reactive({
  version: '',
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
  } catch (error: any) {
    ElMessage.error('获取固件列表失败')
  } finally {
    loading.value = false
  }
}

const handleDelete = async (row: Firmware) => {
  try {
    await ElMessageBox.confirm(
      `确定要删除固件版本 "${formatVersion(row.version)}" 吗？`,
      '提示',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning'
      }
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
  showEditDialog.value = true
}

const handleEditSubmit = async () => {
  if (!editingFirmwareId.value) return
  editing.value = true
  try {
    await firmwareApi.update(editingFirmwareId.value, {
      version: editForm.version
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

.file-info {
  margin-top: 8px;
  font-size: 12px;
  color: #909399;
}

.upload-tip {
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
}
</style>
