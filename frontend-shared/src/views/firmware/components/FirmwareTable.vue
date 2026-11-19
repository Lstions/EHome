<template>
  <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
       10 列合计 ~1500px，390px 视口下必须给横滑提示，否则右侧「操作」列不可达。 -->
  <div class="mobile-table-wrapper">
    <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
    <el-table
      :data="rows"
      stripe
      row-key="id"
      data-testid="firmware-table"
      @selection-change="onSelectionChange"
    >
      <el-table-column type="selection" width="45" />
      <el-table-column label="版本" width="150">
        <template #default="{ row }">
          <el-tag :type="channelTagType(fw(row).channel)" size="small">{{ formatVersion(fw(row).version) }}</el-tag>
          <el-tag v-if="fw(row).withdrawn_at" type="info" size="small" class="withdrawn-tag">已撤回</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="档位（model_key / profile）" width="200" show-overflow-tooltip>
        <template #default="{ row }">
          <span v-if="firmwareProfileText(fw(row))">{{ firmwareProfileText(fw(row)) }}</span>
          <span v-else class="text-muted">未知</span>
        </template>
      </el-table-column>
      <el-table-column label="chip" width="110">
        <template #default="{ row }">
          <span v-if="fw(row).chip">{{ fw(row).chip }}</span>
          <span v-else class="text-muted">未知</span>
        </template>
      </el-table-column>
      <el-table-column label="flash" width="90">
        <template #default="{ row }">
          <span v-if="fw(row).flash_mb">{{ fw(row).flash_mb }} MB</span>
          <span v-else class="text-muted">未知</span>
        </template>
      </el-table-column>
      <el-table-column label="psram" width="90">
        <template #default="{ row }">
          <span v-if="fw(row).psram === 'yes'">有</span>
          <span v-else-if="fw(row).psram === 'no'">无</span>
          <span v-else class="text-muted">未知</span>
        </template>
      </el-table-column>
      <el-table-column label="channel" width="100">
        <template #default="{ row }">{{ firmwareChannelText(fw(row).channel) }}</template>
      </el-table-column>
      <el-table-column label="身份状态" width="140">
        <template #default="{ row }">
          <el-tag :type="identityTagType(fw(row).identity_state)" size="small">
            {{ firmwareIdentityText(fw(row).identity_state) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="文件大小" width="100">
        <template #default="{ row }">{{ formatFileSize(fw(row).size_bytes) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ formatTime(fw(row).created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" min-width="340" class-name="firmware-action-col">
        <template #default="{ row }">
          <div class="action-buttons">
            <el-button type="primary" size="small" @click="emit('edit', fw(row))">
              <el-icon><Edit /></el-icon> 编辑
            </el-button>
            <el-button size="small" @click="emit('copy-url', fw(row))">
              <el-icon><CopyDocument /></el-icon> 复制链接
            </el-button>
            <el-button size="small" @click="emit('download', fw(row))">
              <el-icon><Download /></el-icon> 下载
            </el-button>
            <!-- 撤回是可恢复态（withdrawn_at），已撤回的行不再提供入口 -->
            <el-button
              v-if="!fw(row).withdrawn_at"
              type="warning"
              size="small"
              :title="`撤回 ${formatVersion(fw(row).version)}`"
              @click="emit('withdraw', fw(row))"
            >
              <el-icon><Remove /></el-icon> 撤回
            </el-button>
            <!-- 仅图标的破坏性操作必须有可访问名（B-data 审计 P2） -->
            <el-button
              type="danger"
              size="small"
              aria-label="删除固件"
              title="删除固件"
              @click="emit('delete', fw(row))"
            >
              <el-icon><Delete /></el-icon>
            </el-button>
          </div>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup lang="ts">
import { CopyDocument, Delete, Download, Edit, Remove } from '@element-plus/icons-vue'
import {
  firmwareChannelText,
  firmwareIdentityText,
  firmwareProfileText,
  type Firmware,
} from '@/api/firmware'
import { UNKNOWN } from '@/utils/format'

/**
 * 固件列表（M1b / T1b.3 拆出的子组件）。
 *
 * 列与方案 §4.3「列表列」一致：版本、chip、flash、psram、model_key/build_profile、
 * channel、身份状态 —— **不再有 target_model / stable**（两列已随 M0 迁移删除）。
 * 已撤回的行照常显示（可下载历史），由「已撤回」标签表达；可选性由 OTA 对话框判定。
 */

defineProps<{
  rows: Firmware[]
}>()

const emit = defineEmits<{
  (e: 'selection-change', rows: Firmware[]): void
  (e: 'edit', row: Firmware): void
  (e: 'delete', row: Firmware): void
  (e: 'withdraw', row: Firmware): void
  (e: 'download', row: Firmware): void
  (e: 'copy-url', row: Firmware): void
}>()

/**
 * el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出）。
 * 与 FirmwareManage 既有做法一致：在一个命名边界处收窄，而不是每个表达式各写一次断言。
 */
const fw = (row: unknown) => row as Firmware

const onSelectionChange = (rows: unknown[]) => {
  emit('selection-change', rows as Firmware[])
}

// 版本号统一加 v 前缀（与既有页面口径一致）
const formatVersion = (version: string) => {
  if (!version) return 'v-'
  if (version.startsWith('v') || version.startsWith('V')) return version
  return `v${version}`
}

const channelTagType = (channel?: string) => {
  if (channel === 'stable') return 'success'
  if (channel === 'beta') return 'warning'
  return 'info'
}

const identityTagType = (state?: string) => {
  if (!state || state === 'verified') return 'success'
  return 'warning'
}

const formatFileSize = (bytes: number | undefined | null) => {
  if (!bytes && bytes !== 0) return UNKNOWN
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return UNKNOWN
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return UNKNOWN
  return date.toLocaleString('zh-CN')
}
</script>

<style scoped>
.withdrawn-tag {
  margin-left: 4px;
}
.text-muted {
  color: var(--el-text-color-placeholder);
}
.action-buttons {
  display: flex;
  flex-wrap: nowrap;
  gap: 4px;
}
@media (max-width: 768px) {
  /* 表格列总宽超出视口时，el-table 原生横向滚动，避免列被压缩 */
  .mobile-table-wrapper :deep(.el-table) {
    width: 100%;
  }
  /* P1（B-data 审计）：行首/全选 checkbox 实测热区 14×36（theme.css 只补了高度、
     没补宽度）→ 窄屏补到 36px 宽；视觉 14px 方块不变，只扩热区。 */
  .mobile-table-wrapper :deep(.el-table .el-checkbox) {
    min-width: 36px;
  }
}
/* 粗指针（真机触屏）：与 theme.css §4.4.7 同档，热区补到 44px。 */
@media (pointer: coarse) {
  .mobile-table-wrapper :deep(.el-table .el-checkbox) {
    min-width: 44px;
  }
}
</style>
