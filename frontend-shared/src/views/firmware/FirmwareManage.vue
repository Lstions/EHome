<template>
  <div class="firmware-manage">
    <PageHeader title="固件管理">
      <template #extra>
        <el-button type="primary" :icon="Upload" data-testid="firmware-upload-button" @click="showUploadDialog = true">
          上传固件
        </el-button>
      </template>
    </PageHeader>

    <FirmwareFilterBar :filters="filters" @search="fetchFirmwares" @reset="fetchFirmwares" />

    <el-card>
      <el-skeleton v-if="loading" :rows="5" animated />
      <template v-else>
        <!-- 批量删除：与单条删除同受「在途任务」保护，失败逐条计数 -->
        <div v-if="selectedFirmwares.length > 0" class="batch-bar">
          <span class="batch-info">
            已选择 <strong>{{ selectedFirmwares.length }}</strong> 项
          </span>
          <el-button type="danger" size="small" :icon="Delete" :loading="batchDeleting" @click="handleBatchDelete">
            批量删除
          </el-button>
          <el-button size="small" @click="selectedFirmwares = []">取消选择</el-button>
        </div>

        <!-- 列表：列/筛选/撤回/删除保护见 FirmwareTable 与方案 §4.3 -->
        <FirmwareTable
          v-if="total > 0"
          :rows="pagedFirmwares"
          @selection-change="handleSelectionChange"
          @edit="handleEdit"
          @delete="handleDelete"
          @withdraw="handleWithdraw"
          @download="handleDownload"
          @copy-url="handleCopyUrl"
        />

        <!-- 空态覆盖两种「空」：全库无固件 / 当前筛选无匹配（改前只有第一种有解释） -->
        <EmptyState
          v-if="!loading && total === 0"
          :kind="filtersActive ? 'filtered' : 'empty'"
          :icon="Upload"
          :title="filtersActive ? '没有匹配的固件' : '暂无固件数据'"
          :description="filtersActive
            ? '当前筛选条件下没有固件，可调整或重置筛选。'
            : '上传固件后，可在此管理版本并执行 OTA 升级。'"
          :quick-actions="filtersActive ? [] : [
            { label: '上传固件', icon: Upload, type: 'primary', handler: () => { showUploadDialog = true } }
          ]"
        />

        <!-- 端点不分页（几十条），这里纯前端分页：保留翻页习惯，也避免一次铺满整表 -->
        <div style="display: flex; justify-content: center; margin-top: 16px;" v-if="total > 0">
          <el-pagination
            v-model:current-page="currentPage"
            :page-size="pageSize"
            :total="total"
            layout="total, prev, pager, next"
          />
        </div>
      </template>
    </el-card>

    <FirmwareUploadDialog v-model="showUploadDialog" @uploaded="fetchFirmwares" />
    <FirmwareEditDialog v-model="showEditDialog" :firmware="editingFirmware" @saved="fetchFirmwares" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Delete, Upload } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import FirmwareFilterBar from './components/FirmwareFilterBar.vue'
import FirmwareTable from './components/FirmwareTable.vue'
import FirmwareUploadDialog from './components/FirmwareUploadDialog.vue'
import FirmwareEditDialog from './components/FirmwareEditDialog.vue'
import { firmwareActionErrorMessage, firmwareApi, type Firmware } from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'
import {
  buildFirmwareListParams,
  createFirmwareFilters,
  hasActiveFirmwareFilters,
  paginateFirmwares,
} from './firmwarePage'

/**
 * 固件管理页（M1b / T1b.3）。
 *
 * 页面只做编排：筛选 → 拉取 → 分页 → 分发动作给子组件/对话框。
 * 具体渲染与文案分别在 FirmwareTable / FirmwareFilterBar / *Dialog / firmwarePage 里，
 * 单文件保持 ≤300 行（方案对 T1b.3 的硬要求）。
 *
 * ⚠️ 契约要点（v1.5）：
 * - 列表**不再分页**（后端返回 data: Firmware[] 全量），前端分页仅为体验；
 * - 旧的 status/model 参数已删除，筛选字段见 FirmwareListParams；
 * - 撤回 / 删除都可能 409 FIRMWARE_IN_USE —— 必须把「有在途任务」说清楚（DEF-10）。
 */
const firmwareStore = useFirmwareStore()

const firmwares = ref<Firmware[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)

const filters = reactive(createFirmwareFilters())
const filtersActive = computed(() => hasActiveFirmwareFilters(filters))

const selectedFirmwares = ref<Firmware[]>([])
const batchDeleting = ref(false)

const showUploadDialog = ref(false)
const showEditDialog = ref(false)
const editingFirmware = ref<Firmware | null>(null)

const total = computed(() => firmwares.value.length)
const pagedFirmwares = computed(() => paginateFirmwares(firmwares.value, currentPage.value, pageSize.value))

const fetchFirmwares = async () => {
  loading.value = true
  try {
    await firmwareStore.fetchList(buildFirmwareListParams(filters), true)
    firmwares.value = firmwareStore.list
    // 筛选变化后页码可能越界：类型断言由 paginateFirmwares 内部钳制，这里同步回状态
    if (currentPage.value > 1 && pagedFirmwares.value.length === 0) currentPage.value = 1
  } catch (error) {
    feedback.handleError(error, '获取固件列表失败')
  } finally {
    loading.value = false
  }
}

const handleSelectionChange = (rows: Firmware[]) => {
  selectedFirmwares.value = rows
}

const handleBatchDelete = async () => {
  const count = selectedFirmwares.value.length
  if (!(await feedback.confirmDanger(
    `确定要删除选中的 ${count} 个固件吗？此操作不可恢复。`,
    { title: '批量删除', confirmText: '删除', cancelText: '取消' }
  ))) return

  try {
    batchDeleting.value = true
    const ids = selectedFirmwares.value.map(f => f.id)
    const results = await Promise.allSettled(ids.map(id => firmwareStore.deleteFirmware(id)))
    const succeeded = results.filter(result => result.status === 'fulfilled').length
    const failed = results.length - succeeded
    selectedFirmwares.value = []
    await fetchFirmwares()
    if (succeeded > 0) ElMessage.success(`已删除 ${succeeded} 个固件`)
    if (failed > 0) ElMessage.warning(`${failed} 个固件删除失败（可能有在途 OTA 任务）`)
  } catch (error) {
    feedback.handleErrorWithContext(error, '批量删除固件失败')
  } finally {
    batchDeleting.value = false
  }
}

const handleEdit = (row: Firmware) => {
  editingFirmware.value = row
  showEditDialog.value = true
}

const handleDelete = async (row: Firmware) => {
  // 删除固件版本不可恢复：确认框必须带对象身份与不可逆影响，且确认按钮为 danger。
  const confirmed = await feedback.confirmDanger(
    `确定要删除固件版本 "${formatVersion(row.version)}" 吗？此操作不可恢复。`,
    { title: '删除固件', confirmText: '删除', cancelText: '取消' }
  )
  if (!confirmed) return

  try {
    await firmwareStore.deleteFirmware(row.id)
    await fetchFirmwares()
    ElMessage.success('删除成功')
  } catch (error) {
    // 409 FIRMWARE_IN_USE 必须说清「为什么不让删」，而不是「删除失败」
    feedback.error(firmwareActionErrorMessage(error, 'delete'))
  }
}

const handleWithdraw = async (row: Firmware) => {
  // 撤回是「可恢复但会立即改变可用集合」的操作：用 danger 确认（红色确认键 + 默认焦点在取消），
  // 与删除同一出口 —— 列表页约定门禁只允许 feedback.confirmDanger 作为危险确认出口。
  const confirmed = await feedback.confirmDanger(
    `确定撤回固件 "${formatVersion(row.version)}" 吗？撤回后不能再用于新的 OTA 升级（已在途任务不受影响，可随时恢复）。`,
    { title: '撤回固件', confirmText: '撤回', cancelText: '取消' }
  )
  if (!confirmed) return

  try {
    await firmwareStore.withdraw(row.id)
    await fetchFirmwares()
    ElMessage.success('已撤回')
  } catch (error) {
    feedback.error(firmwareActionErrorMessage(error, 'withdraw'))
  }
}

/** 复制文本：优先 Clipboard API，非安全上下文退回 execCommand。 */
async function copyTextToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const textarea = document.createElement('textarea')
    textarea.value = text
    textarea.style.position = 'fixed'
    textarea.style.opacity = '0'
    document.body.appendChild(textarea)
    textarea.select()
    try {
      document.execCommand('copy')
      return true
    } catch {
      return false
    } finally {
      document.body.removeChild(textarea)
    }
  }
}

/**
 * 复制下载链接：先向已鉴权接口换一张 30 分钟票。
 *
 * 旧实现把登录 JWT 拼进 URL，而公开下载路由只认 HMAC 票据 —— 复制出来的链接
 * 必然 401，且把长期凭证泄漏到剪贴板。换票后链接自带有效期，登录 token 不出前端。
 */
const handleCopyUrl = async (row: Firmware) => {
  try {
    const ticket = await firmwareApi.createDownloadTicket(row.id)
    if (!(await copyTextToClipboard(ticket.url))) {
      feedback.error('复制失败，请手动复制: ' + ticket.url)
      return
    }
    ElMessage.success('下载链接已复制（30 分钟内有效）')
  } catch (error) {
    feedback.error(firmwareActionErrorMessage(error, 'copy-url'))
  }
}

/** 下载：换票后由浏览器打开票据 URL（不再把 JWT 塞进 URL）。 */
const handleDownload = async (row: Firmware) => {
  try {
    const ticket = await firmwareApi.createDownloadTicket(row.id)
    const link = document.createElement('a')
    link.href = ticket.url
    link.download = `firmware_${formatVersion(row.version)}.bin`
    link.rel = 'noopener'
    link.click()
    ElMessage.success('开始下载')
  } catch (error) {
    feedback.error(firmwareActionErrorMessage(error, 'download'))
  }
}

// 版本号格式化：统一加 v 前缀
const formatVersion = (version: string) => {
  if (!version) return 'v-'
  if (version.startsWith('v') || version.startsWith('V')) return version
  return `v${version}`
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

@media (max-width: 768px) {
  /* 分页在窄屏下换行并居中 */
  .firmware-manage :deep(.el-pagination) {
    flex-wrap: wrap;
    justify-content: center;
  }
}
</style>
