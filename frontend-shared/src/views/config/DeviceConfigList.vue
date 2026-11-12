<template>
  <div class="config-page">
    <!-- 页头（I-6 标题统一，范式同 views/data-source/DataSourceList.vue）。
         副标题取自本页空状态既有文案（见下方 EmptyState 的 description），未新造措辞。 -->
    <PageHeader title="配置模板" subtitle="为边缘设备复用连接、解析与初始化配置" />

    <!-- 顶部统计 -->
    <div class="stats-row">
      <StatCard :label="`模板总数（${SCOPE_FILTERED}）`" icon-color="var(--el-color-primary)">
        <template #icon><el-icon><Document /></el-icon></template>
        <template #value><span class="stat-value">{{ stats.total }}</span></template>
      </StatCard>

      <StatCard :label="`${SCOPE_PAGE}启用`" icon-color="var(--el-color-success)">
        <template #icon><el-icon><CircleCheck /></el-icon></template>
        <template #value><span class="stat-value">{{ stats.active }}</span></template>
      </StatCard>

      <StatCard :label="`总线类型（${SCOPE_PAGE}）`" icon-color="var(--el-color-info)">
        <template #icon><el-icon><Connection /></el-icon></template>
        <template #value><span class="stat-value">{{ stats.busTypes }}</span></template>
      </StatCard>

      <StatCard :label="`设备类型（${SCOPE_PAGE}）`" icon-color="var(--el-color-warning)">
        <template #icon><el-icon><Cpu /></el-icon></template>
        <template #value><span class="stat-value">{{ stats.deviceTypes }}</span></template>
      </StatCard>
    </div>

    <!-- 工具栏 -->
    <DeviceConfigToolbar
      v-model:keyword="searchKeyword"
      v-model:type="typeFilter"
      v-model:hardware="hardwareFilter"
      v-model:status="statusFilter"
      @search="handleSearch"
    >
      <template #actions>
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
      </template>
    </DeviceConfigToolbar>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''; handleFiltersChanged()">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="typeFilter" closable @close="typeFilter = ''; handleFiltersChanged()">类型：{{ getDeviceTypeLabel(typeFilter) }}</el-tag>
      <el-tag v-if="hardwareFilter" closable @close="hardwareFilter = ''; handleFiltersChanged()">总线：{{ getHardwareLabel(hardwareFilter) }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''; handleFiltersChanged()">状态：{{ statusFilter === 'active' ? '启用' : '禁用' }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 配置模板卡片 -->
    <!-- F3：加载态收敛 —— 本页原先**没有任何加载指示**（有 loading ref，却既无 v-loading
         也无骨架）：首屏请求未回来时 .config-grid 为空、空态又因 `!loading` 被抑制，
         于是加载期间只有一片空白，用户无法区分"正在加载"与"页面坏了"。
         这里按仓库惯例（DataSourceList.vue:102 等）用 v-loading，与下方
         `v-if="!loading && ..."` 的空态口径一致。 -->
    <div class="config-grid" v-loading="loading" data-test="config-grid">
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

        <DeviceConfigCardSpecs :config="config" />

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
              <DeviceConfigCardMoreMenu :config="config" />
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
    <DeviceConfigPreviewDialog v-model="previewVisible" :config="previewConfig" />

    <!-- 创建/编辑对话框 -->
    <DeviceConfigForm
      v-model:visible="showFormDialog"
      :config="editingConfig"
      @success="handleFormSuccess"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import {
  Document, CircleCheck, Connection, Cpu,
  Upload, Download, Plus, View, Edit, CopyDocument, MoreFilled,
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import DeviceConfigForm from '@/components/forms/DeviceConfigForm.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import StatCard from '@/components/common/StatCard.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import DeviceConfigCardSpecs from './components/DeviceConfigCardSpecs.vue'
import DeviceConfigCardMoreMenu from './components/DeviceConfigCardMoreMenu.vue'
import DeviceConfigPreviewDialog from './components/DeviceConfigPreviewDialog.vue'
import DeviceConfigToolbar from './components/DeviceConfigToolbar.vue'
import { getBusIcon, getDeviceTypeLabel } from './deviceConfigDisplay'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import { useDeviceConfigList } from './composables/useDeviceConfigList'
import { useDeviceConfigActions } from './composables/useDeviceConfigActions'
import { useDeviceConfigTransfer } from './composables/useDeviceConfigTransfer'

/**
 * F14 统计卡范围词（规范 §4.3 MUST）。
 *   · SCOPE_FILTERED —— `stats.total ← total.value ← response.total ← 后端 q.Count(&total)`
 *     （handler_device.go:233-234，q 已按 device_type/hardware_type/status 过滤），
 *     口径是「当前筛选条件下的全量」。
 *   · 其余三项（active/busTypes/deviceTypes）由 `configs.value`（当前页 pageSize 条）算出，
 *     已在标签里写作「本页启用」；busTypes/deviceTypes 此前只有名词，同样必须标出本页。
 * 不规范标注时：「模板总数=137」而「总线类型」只反映本页出现过的类型，用户会以为是全站类型数。
 *
 * 常量的解析者是 statCardScopeGate.spec.ts（它从**同一份源码**里读 SCOPE_* 与 <StatCard>），
 * 故这两个常量必须留在页面文件内，不能随逻辑搬进 composable。
 */
const SCOPE_FILTERED = '当前筛选'
const SCOPE_PAGE = '本页'

// 列表状态 / 请求 / 筛选 / 统计：全部搬进 useDeviceConfigList（页面只接线）。
const {
  loading, currentPage, pageSize, total,
  searchKeyword, typeFilter, hardwareFilter, statusFilter,
  hasActiveFilters, stats, filteredConfigs,
  fetchConfigs, handleSearch, handleFiltersChanged, clearFilters,
} = useDeviceConfigList()

// 预览·表单对话框状态：搬进 useDeviceConfigActions。
const {
  showFormDialog, previewVisible, editingConfig, previewConfig,
  handlePreview, handleEdit, handleFormSuccess,
} = useDeviceConfigActions({ fetchConfigs })

// 导入 / 导出（单条、批量）：搬进 useDeviceConfigTransfer。
const { exportConfig, importConfig, exportConfigs } = useDeviceConfigTransfer({ fetchConfigs, filteredConfigs })

// 克隆
// 留在页面：listPageConventions.spec.ts 的 EXEMPT_BARE_CONFIRM 按页面路径登记了
// 这里的 feedback.confirm（非破坏性确认），路径登记必须在页面里仍有对应调用。
const handleClone = async (config: DeviceConfig) => {
  // 克隆是**非破坏性**操作 ⇒ 走 feedback.confirm (普通确认)。
  // 不得改走 confirmDanger: §3.4.3 的 danger 类目只用于破坏性操作, 给"克隆"的确认键
  // 染 danger 红是语义错误 (本仓既有结构性裁决, 见 listPageConventions.spec.ts)。
  // confirm 返回 boolean, 取消即 return —— 不再依赖 error !== 'cancel' 字符串判断。
  const ok = await feedback.confirm(`确定要克隆配置 "${config.name}" 吗？`, {
    title: '克隆配置',
    confirmText: '确定',
    cancelText: '取消',
  })
  if (!ok) return

  try {
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
    feedback.handleError(error, '克隆失败')
  }
}

// 更多操作
// 分发与危险确认刻意留在页面：listPageConventions.spec.ts 的「危险操作必须二次确认」
// 按**页面文件**扫描 deviceConfigApi.delete 与 feedback.confirmDanger —— 搬进 composable
// 会让本页退出该规则的射程（规则仍绿，但已经扫不到这里）。
const handleMoreAction = async (command: string, config: DeviceConfig) => {
  switch (command) {
    case 'setDefault':
      try {
        await deviceConfigApi.setDefault(config.id)
        ElMessage.success('设置成功')
        await fetchConfigs()
      } catch (error) {
        feedback.handleError(error, '设置失败')
      }
      break

    case 'toggle': {
      // 目标状态必须在 try **外**算出来：catch 里要用它决定失败文案
      // （放在 try 内会被块级作用域挡住，vue-tsc 会报 TS2552）。
      const newStatus = config.status === 'active' ? 'inactive' : 'active'
      try {
        // 后端 PUT 要求 name 必填（缺失即 400），且以其当前行为底合并，故只发 name + status。
        await deviceConfigApi.update(config.id, { name: config.name, status: newStatus })
        ElMessage.success(newStatus === 'active' ? '已启用' : '已禁用')
        await fetchConfigs()
      } catch (error) {
        // 同文件其余动作（克隆/设置默认/导入）都带宾语，此处原先只写「操作失败」——
        // 而后端总会带 message，用户看不到失败的是「启用/禁用」这件事。
        feedback.handleErrorWithContext(error, newStatus === 'active' ? '启用失败' : '禁用失败')
      }
      break
    }

    case 'export':
      exportConfig(config)
      break

    case 'delete': {
      // 删除配置模板不可恢复：走统一危险确认（danger 确认键 + 焦点不落在破坏性按钮上）。
      // 取消时直接跳出，不再进入下面的 try，故 catch 只处理接口失败（与迁移前一致）。
      const confirmed = await feedback.confirmDanger(
        `确定要删除配置 "${config.name}" 吗？`,
        { title: '警告', confirmText: '删除', cancelText: '取消' },
      )
      if (!confirmed) break

      try {
        await deviceConfigApi.delete(config.id)
        ElMessage.success('删除成功')
        await fetchConfigs()
      } catch {
        feedback.error('删除失败')
      }
      break
    }
  }
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
  background: var(--card-bg);
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
  /* 白字：基类前景；具体底色由 .stat-icon.* / .config-icon.* 变体决定（见各变体规则）。 */
  color: #fff;
  font-size: 20px;
  background: var(--brand-gradient);
}

.stat-icon.active { background: var(--el-color-success); color: var(--text-on-fill); }
.stat-icon.bus { background: var(--el-color-warning); color: var(--text-on-fill); }
.stat-icon.device { background: var(--el-text-color-secondary); color: var(--text-on-fill); }

.stat-content { flex: 1; }
.stat-value { display: block; font-size: 24px; font-weight: 600; color: var(--el-text-color-primary); }
.stat-label { font-size: 13px; color: var(--el-text-color-secondary); }

/* 工具栏右侧动作组（模板+样式留在页面：DeviceConfigList.spec 直接断言页面 scoped CSS） */
.filter-right {
  display: flex;
  gap: 8px;
  /* 三个操作按钮（导入/导出/新建模板）总宽 261px，360px 视口下 .filter-bar 可用宽仅 270px。
     不允许换行时「导入」被推出卡片内容区左边界，且祖先链 overflow 全为 visible/auto 且
     scrollWidth === clientWidth（scrollable:false）⇒ 真实裁切而非可滚动溢出。
     .filter-bar / .filter-left 均已声明 flex-wrap: wrap，此处补齐同一约束。 */
  flex-wrap: wrap;
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
  box-shadow: var(--shadow-lg);
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
  /* 白字：基类前景；具体底色由 .stat-icon.* / .config-icon.* 变体决定（见各变体规则）。 */
  color: #fff;
  flex-shrink: 0;
}

.config-icon.uart { background: var(--brand-gradient); }
.config-icon.i2c { background: linear-gradient(135deg, var(--el-color-warning) 0%, var(--el-color-danger) 100%); color: var(--text-on-fill); }
.config-icon.spi { background: linear-gradient(135deg, var(--el-text-color-secondary) 0%, var(--el-text-color-placeholder) 100%); color: var(--text-on-fill); }
.config-icon.gpio { background: linear-gradient(135deg, var(--el-color-success) 0%, var(--el-color-success-light-3) 100%); color: var(--text-on-fill); }
.config-icon.adc { background: linear-gradient(135deg, var(--color-adc) 0%, var(--color-adc-light) 100%); color: var(--text-on-fill); }

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

.card-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 12px;
  /* 360px 实测：.el-card__body 内容宽 270px，四个按钮（预览 69.02 + 克隆 69.02 +
     编辑 69.02 + 下拉 39）加 item 间距 294.05px > 270px。右对齐 + 不换行时
     「预览」被推出 .el-card__body 左界（x=16.95 < body x=21，裁 4.05px），
     而 body/card/grid/el-main 的 scrollWidth === clientWidth（不可回滚）⇒ 真实裁切。
     F9 只补了 .filter-right，漏了同一根因的 .card-footer，此处补齐。 */
  flex-wrap: wrap;
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

/* 响应式：中屏 2 列；移动端单行 4 列紧凑横排（与 NodeList/EdgeDeviceList 一致，占高约一行） */
@media (max-width: 1200px) {
  .stats-row { grid-template-columns: repeat(2, 1fr); }
}

@media (max-width: 768px) {
  .stats-row { grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
  /* 覆盖桌面 .stat-card 基础块（padding 20px/图标 48px），单行 4 列纵向紧凑小卡 */
  .stat-card { flex-direction: column; align-items: center; text-align: center; gap: 4px; padding: 8px 4px; border-radius: 10px; }
  .stat-icon { width: 22px; height: 22px; border-radius: 6px; font-size: 13px; flex-shrink: 0; }
  .stat-content { width: 100%; display: flex; flex-direction: column; align-items: center; gap: 1px; }
  .stat-value { font-size: 16px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
  .stat-label { font-size: 10px; line-height: 1.3; max-height: 2.6em; overflow: hidden; word-break: keep-all; overflow-wrap: break-word; }
  .filter-right { width: 100%; }
  .filter-right { justify-content: flex-end; }
  .config-grid { grid-template-columns: 1fr; }
}

</style>
