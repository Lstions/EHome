<template>
  <div class="overview-tab">
    <!--
      只读提示：这不是"功能受限"，而是本页的**设计事实** ——
      数据由系统自动派生，用户不需要也不能维护。
    -->
    <el-alert type="info" :closable="false" show-icon class="ov-notice" data-test="ov-readonly-notice">
      <template #title>
        本页由设备与采集数据<strong>自动生成</strong>，无需手工维护。
        需要主备切换或复合来源，请到<el-button link type="primary" data-test="ov-goto-groups" @click="$emit('open-group')">
          「来源组」
        </el-button>配置。
      </template>
    </el-alert>

    <el-alert v-if="error" type="error" :closable="false" show-icon class="ov-error" data-test="ov-error">
      <template #title>
        <span>加载来源总览失败：{{ error }}</span>
        <el-button link type="primary" size="small" data-test="ov-retry" @click="$emit('retry')">重试</el-button>
      </template>
    </el-alert>

    <!--
      移动端横滚容器：本表 8 列，窄屏必然溢出。
      本仓有门禁（MobileTableWrapper.spec.ts）要求每张 el-table 必须在
      .mobile-table-wrapper 祖先链上，否则窄屏用户看不到右侧列。
    -->
    <div class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        v-loading="loading"
        :data="items"
        class="ov-table"
        data-test="ov-table"
        empty-text="暂无来源 —— 设备上报数据后会自动出现"
      >
      <el-table-column label="逻辑设备" min-width="150">
        <template #default="{ row }">
          <span>{{ asItem(row).logical_device_name || '—' }}</span>
          <span class="mono muted">#{{ asItem(row).logical_device_id }}</span>
        </template>
      </el-table-column>

      <el-table-column label="数据类别" prop="category" min-width="150">
        <template #default="{ row }">
          <span class="mono">{{ asItem(row).category }}</span>
        </template>
      </el-table-column>

      <el-table-column label="来源类型" width="130">
        <template #default="{ row }">
          <el-tag :type="kindTagType(asItem(row).kind)" size="small" data-test="ov-kind">
            {{ kindLabel(asItem(row).kind) }}
          </el-tag>
        </template>
      </el-table-column>

      <el-table-column label="成员" width="90" align="center">
        <template #default="{ row }">
          <span data-test="ov-member-count">{{ asItem(row).member_count }}</span>
        </template>
      </el-table-column>

      <el-table-column label="当前来源" min-width="200">
        <template #default="{ row }">
          <div class="ov-members">
            <span
              v-for="m in asItem(row).members"
              :key="m.edge_device_id"
              class="ov-member"
              :class="{ 'is-active': m.active, 'is-disabled': !m.enabled }"
            >
              {{ m.edge_device_name || '#' + m.edge_device_id }}
              <el-tag v-if="m.active" type="success" size="small" effect="plain">权威</el-tag>
            </span>
          </div>
        </template>
      </el-table-column>

      <el-table-column label="来源组" min-width="140">
        <template #default="{ row }">
          <el-button
            v-if="asItem(row).group_id > 0"
            link
            type="primary"
            data-test="ov-group-link"
            @click="$emit('open-group')"
          >
            {{ asItem(row).group_name || '#' + asItem(row).group_id }}
          </el-button>
          <span v-else class="muted">单设备直连</span>
        </template>
      </el-table-column>

      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="statusTagType(asItem(row).status)" size="small" data-test="ov-status">
            {{ statusLabel(asItem(row).status) }}
          </el-tag>
        </template>
      </el-table-column>

      <el-table-column label="最后成功" width="170">
        <template #default="{ row }">
          <span class="mono muted">{{ fmtTime(asItem(row).last_success) }}</span>
        </template>
      </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 来源总览 Tab（只读）。
 *
 * # 只读是如何保证的（三层，不只靠 UI）
 *
 * 1. **本组件契约**：props 只有数据、emits 只有 open-group/retry ——
 *    类型层就没有 create/update/delete 的能力。
 * 2. **前端单测**：断言本组件渲染后不存在任何写按钮（见 __tests__）。
 * 3. **后端路由**：GET /data-sources/overview 的写方法返回 405。
 *
 * 三层任一层失守都还有另外两层兜底 —— 这是"只读"可信的原因。
 */
import type { SourceKind, SourceOverviewItem, SourceStatus } from '@/api/sourceOverview'

defineProps<{
  items: SourceOverviewItem[]
  loading: boolean
  error: string
}>()

defineEmits<{
  (e: 'open-group'): void
  (e: 'retry'): void
}>()

/** el-table 的 slot 里 row 是 unknown，需要显式收窄（与项目其他列表页同模式）。 */
const asItem = (row: unknown) => row as SourceOverviewItem

function kindLabel(k: SourceKind): string {
  switch (k) {
    case 'primary_backup':
      return '主备'
    case 'composite':
      return '复合'
    default:
      return '直连'
  }
}

function kindTagType(k: SourceKind): 'info' | 'success' | 'warning' {
  switch (k) {
    case 'primary_backup':
      return 'success'
    case 'composite':
      return 'warning'
    default:
      return 'info'
  }
}

function statusLabel(s: SourceStatus): string {
  switch (s) {
    case 'ok':
      return '正常'
    case 'degraded':
      return '降级'
    case 'disabled':
      return '已停用'
    default:
      return '未配置'
  }
}

function statusTagType(s: SourceStatus): 'success' | 'warning' | 'info' | 'danger' {
  switch (s) {
    case 'ok':
      return 'success'
    case 'degraded':
      return 'warning'
    case 'disabled':
      return 'info'
    default:
      return 'info'
  }
}

function fmtTime(v: string | null): string {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString('zh-CN', { hour12: false })
}
</script>

<style scoped>
.overview-tab {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.ov-members {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.ov-member {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--el-fill-color-light);
  font-size: 12px;
}
.ov-member.is-active {
  background: var(--el-color-success-light-9);
}
.ov-member.is-disabled {
  opacity: 0.5;
  text-decoration: line-through;
}
.mono {
  font-family: var(--el-font-family-mono, monospace);
}
.muted {
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}
</style>
