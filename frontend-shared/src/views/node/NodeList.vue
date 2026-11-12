<template>
  <div class="collector-page">
<template v-if="loading">
      <div class="stats-row">
        <SkeletonCard v-for="i in 4" :key="i" variant="stat" :icon-size="48" animated />
      </div>
      <div class="skeleton-grid">
        <SkeletonCard v-for="i in 8" :key="i" variant="card" animated />
      </div>
    </template>
    <template v-else>
      <!-- 顶部统计卡片 -->
      <div class="stats-row">
        <StatCard label="本页节点" icon-color="var(--el-color-primary)" @click="handleStatClick('all')">
          <template #icon><el-icon><Connection /></el-icon></template>
          <template #value><CountUp :value="stats.total" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页在线" icon-color="var(--el-color-success)" @click="handleStatClick('online')">
          <template #icon><el-icon><CircleCheck /></el-icon></template>
          <template #value><CountUp :value="stats.online" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页离线" icon-color="var(--el-color-danger)" @click="handleStatClick('offline')">
          <template #icon><el-icon><CircleClose /></el-icon></template>
          <template #value><CountUp :value="stats.offline" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页告警" icon-color="var(--el-color-warning)">
          <template #icon><el-icon><Warning /></el-icon></template>
          <template #value><CountUp :value="stats.warning" class="stat-value" /></template>
        </StatCard>
      </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索名称/型号"
          prefix-icon="Search"
          clearable
          class="search-input"
          @input="handleSearch"
        />
        
        <el-select v-model="statusFilter" placeholder="状态筛选" clearable style="min-width: 120px;">
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部" value="" />
          <el-option label="在线" value="online" />
          <el-option label="离线" value="offline" />
        </el-select>
        
        <!-- 型号选项来自**当前页**的 model 去重 (服务端无独立 facet 端点)。
             不是全库清单, 因此改选型号后需用搜索框做全库检索。 -->
        <el-select v-model="modelFilter" placeholder="型号筛选" clearable style="min-width: 120px;">
          <el-option v-for="model in modelOptions" :key="model" :label="model" :value="model" />
        </el-select>
      </div>
      
      <div class="toolbar-right">
        <el-button-group>
          <el-button :type="viewMode === 'grid' ? 'primary' : 'default'" aria-label="卡片视图" @click="viewMode = 'grid'">
            <el-icon><Grid /></el-icon>
          </el-button>
          <el-button :type="viewMode === 'list' ? 'primary' : 'default'" aria-label="表格视图" @click="viewMode = 'list'">
            <el-icon><List /></el-icon>
          </el-button>
        </el-button-group>
        
        <el-button type="primary" @click="openAddDialog">
          <el-icon><Plus /></el-icon>
          添加节点
        </el-button>
        <el-button type="primary" @click="refreshData" :loading="refreshing">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </div>
    </div>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''">状态：{{ statusFilter === 'online' ? '在线' : '离线' }}</el-tag>
      <el-tag v-if="modelFilter" closable @close="modelFilter = ''">型号：{{ modelFilter }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 卡片视图 -->
    <!-- 单卡搬进 components/NodeGridCard.vue（只依赖一个 node 对象）；
         列表状态仍由本页 composable 持有，卡片把动作原样上抛。 -->
    <div v-if="viewMode === 'grid'" class="collector-grid">
      <NodeGridCard
        v-for="node in filteredNodes"
        :key="node.id"
        :node="node"
        @open="goToDetail"
        @quick-action="handleQuickAction"
        @delete="handleDelete"
      />
    </div>

    <!-- 列表视图 -->
    <el-card v-else class="collector-table-card" shadow="hover">
      <!-- 宽表（含 .mobile-table-wrapper 横滑容器与提示）搬进 components/NodeTable.vue；
           分页器必须留在页面 —— listPageConventions 以「页面模板里有 el-pagination」定列表页分母。 -->
      <NodeTable
        :nodes="filteredNodes"
        :loading="loading"
        @open="goToDetail"
        @delete="handleDelete"
      />

      <div class="pagination-wrapper">
        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="() => fetchNodes()"
          @size-change="handlePageSizeChange"
        />
      </div>
    </el-card>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && filteredNodes.length === 0"
      icon="Connection"
      title="暂无节点"
      description="开始添加第一个节点来监控您的设备"
      :quick-actions="[
        { label: '添加节点', icon: Plus, type: 'primary', handler: openAddDialog }
      ]"
    />

    </template>

    <!-- 两个对话框挂在页面级、**不进 `v-else` 分支**：加载骨架屏期间组件同样存在，
         深链 ?action=add 一进来就能弹（放进 v-else 会等到骨架屏消失才出现）。 -->

    <!-- 添加节点（手动注册）：POST /api/v1/nodes 的真实入口。
         改前工具栏按钮 push('/node?action=add')、空态快捷动作只弹一句
         ElMessage.info('跳转添加页面'), 两者全仓都没有消费者 —— 有按钮、没落点。 -->
    <el-dialog
      v-model="addDialogVisible"
      title="添加节点"
      width="480px"
      :close-on-click-modal="false"
      @closed="resetAddForm"
    >
      <el-form label-width="96px" @submit.prevent>
        <el-form-item label="设备 ID" required>
          <el-input
            v-model="addForm.node_id"
            placeholder="F0F5BD02F35C"
            maxlength="12"
            clearable
            @keyup.enter="submitAddNode"
          />
          <div class="form-hint">12 位十六进制 MAC，例：F0F5BD02F35C</div>
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="addForm.name" placeholder="可选，留空用设备 ID" @keyup.enter="submitAddNode" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="addDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="addSubmitting" @click="submitAddNode">确定</el-button>
      </template>
    </el-dialog>

    <!-- OTA 升级对话框：复用生产组件 OTAForm（与 NodeOverview 同一契约），
         改前这里 push('/firmware?node=X')，而 FirmwareManage.vue 0 命中
         useRoute/route.query —— 升级按钮是死入口。 -->
    <OTAForm
      :visible="otaDialogVisible"
      :collector-id="otaNode.node_id"
      :collector-model="otaNode.model"
      :current-firmware-version="otaNode.firmware_version"
      @success="handleOTASuccess"
      @update:visible="otaDialogVisible = $event"
    />
  </div>
</template>

<script setup lang="ts">
import {
  Connection, CircleCheck, CircleClose, Warning,
  Filter, Grid, List, Refresh, Plus
} from '@element-plus/icons-vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'
import StatCard from '@/components/common/StatCard.vue'
import OTAForm from '@/components/forms/OTAForm.vue'
import NodeGridCard from './components/NodeGridCard.vue'
import NodeTable from './components/NodeTable.vue'
import { useNodeList } from './composables/useNodeList'

/**
 * 本页只做编排：列表状态与动作在 composables/useNodeList.ts；卡片/宽表两个自包含
 * 模板片段在 components/（通过事件回传动作，不各自持有列表状态）。
 * 骨架屏、统计卡、工具栏、筛选标签、分页器与两个对话框必须留在**页面文件**里 ——
 * DashboardSkeletonGeometry / FillContrastContract / listPageConventions 等源码门禁
 * 直接读 NodeList.vue 定位它们（含「页面模板必须有 el-pagination」的列表页口径）。
 */
const {
  currentPage, pageSize, total, viewMode,
  searchKeyword, statusFilter, modelFilter,
  hasActiveFilters, modelOptions, filteredNodes, stats,
  loading, refreshing,
  addDialogVisible, addForm, addSubmitting,
  otaDialogVisible, otaNode,
  fetchNodes, refreshData,
  handleSearch, handlePageSizeChange, clearFilters, handleStatClick,
  openAddDialog, resetAddForm, submitAddNode,
  handleOTASuccess, handleQuickAction, handleDelete, goToDetail,
} = useNodeList()
</script>

<style scoped>
.collector-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

/* 统计卡片 */
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
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

.stat-icon {
  width: 56px;
  height: 56px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 28px;
  color: var(--el-text-color-secondary);
}

.stat-card.total .stat-icon { color: var(--el-color-primary); }
.stat-card.online .stat-icon { color: var(--el-color-success); }
.stat-card.offline .stat-icon { color: var(--el-color-danger); }
.stat-card.warning .stat-icon { color: var(--el-color-warning); }

/* ⚠️ 死代码：.stat-content 在模板中未被使用（仅 style 命中）。
   留着会误导后人以为这里有"白字压深底"的设计，实际不存在。 */
.stat-content {
  align-items: center;
  justify-content: center;
  /* 死代码：本块在模板中未被使用（仅 style 命中），见上方说明。 */
  color: #fff;
  font-size: 20px;
}

.stat-content {
  flex: 1;
}

.stat-value {
  display: block;
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  line-height: 1.2;
}

.stat-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}


/* 工具栏 */
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: var(--el-bg-color);
  padding: 16px 20px;
  border-radius: 12px;
  border: 1px solid var(--el-border-color);
}

.toolbar-left {
  display: flex;
  gap: 12px;
}

.search-input {
  width: 280px;
  min-width: 200px;
}

.toolbar-right {
  display: flex;
  gap: 12px;
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

/* 卡片网格 */
.collector-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

/* 列表视图 */
.collector-table-card :deep(.el-table) {
  border-radius: 8px;
}

.pagination-wrapper {
  margin-top: 20px;
  display: flex;
  justify-content: flex-end;
}

/* 骨架屏 */
.skeleton-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

/* 空状态 */
.empty-state {
  padding: 60px 0;
}

/* 响应式：中屏 2 列；移动端单行 4 列紧凑横排（占高约一行，让位给列表） */
@media (max-width: 1200px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }

  /* 覆盖桌面 .stat-card 基础块（padding 20px/图标 56px），单行 4 列纵向紧凑小卡 */
  .stat-card {
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 4px;
    padding: 8px 4px;
    border-radius: 10px;
  }
  .stat-icon {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    font-size: 13px;
    flex-shrink: 0;
  }
  .stat-content {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
  }
  .stat-value {
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }
  .stat-label {
    font-size: 10px;
    line-height: 1.3;
    max-height: 2.6em;
    overflow: hidden;
    word-break: keep-all;
    overflow-wrap: break-word;
  }

  .toolbar {
    flex-direction: column;
    gap: 12px;
  }

  .toolbar-left, .toolbar-right {
    width: 100%;
    flex-wrap: wrap;
  }

  .search-input {
    width: 100%;
  }
}

/* 添加节点对话框的字段说明（设备 ID 格式要求） */
.form-hint {
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
}
</style>
