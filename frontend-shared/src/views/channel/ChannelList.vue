<template>
  <div class="channel-page">
    <!-- A-nodes P2（2026-10-21，finding #38）：原副标题承诺「查看和管理」，
         但本页没有新增/编辑通道入口（ChannelManager 只挂在节点详情）。
         不改能力边界、只让文案与能力对齐，并指出真实入口在哪。 -->
    <PageHeader title="通道管理" subtitle="查看所有节点的通道配置；新增/编辑通道请到节点详情 → 总线配置">
      <template #extra>
        <el-button @click="refreshData" :loading="loading">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </template>
    </PageHeader>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索通道名称、硬件ID..."
          prefix-icon="Search"
          clearable
          class="search-input"
          @input="handleSearch"
        />
        <!-- 范围词（规范 §4.3 MUST）：关键词是**客户端**过滤，只作用于服务端返回的当前页。
             不标注范围，用户会把"本页没搜到"当成"全库没有"——那是伪造的领域事实。 -->
        <span class="search-scope-hint" data-test="channel-search-scope">{{ searchScopeHint }}</span>

        <el-select
          v-model="nodeFilter"
          placeholder="节点筛选"
          clearable
          class="filter-select"
          @change="handleFilter"
        >
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部节点" value="" />
          <!-- :value 必须是**物理序列号** node.node_id，不是 nodes 表自增主键 node.id：
               后端按 node_id 过滤（先试 ParseUint，失败则按 nodes.node_id 查），
               而主键 number 与 ch.node_id(string) 恒不相等 —— 旧写法选中后恒返回 0 行。 -->
          <el-option
            v-for="node in nodeOptions"
            :key="node.node_id"
            :label="nodeDisplayName(node)"
            :value="node.node_id"
          />
        </el-select>

        <el-select
          v-model="hardwareTypeFilter"
          placeholder="硬件类型筛选"
          clearable
          class="filter-select"
          @change="handleFilter"
        >
          <template #prefix>
            <el-icon><Filter /></el-icon>
          </template>
          <el-option label="全部类型" value="" />
          <el-option label="UART" value="uart" />
          <el-option label="I2C" value="i2c" />
          <el-option label="SPI" value="spi" />

          <el-option label="ADC" value="adc" />
        </el-select>

        <!-- 节点下拉按页长上界一次拉取；节点总数超过上界时必须**显式**提示已截断，
             否则"选不到某个节点"会变成无解释的静默截断。 -->
        <el-alert
          v-if="nodeFilterTruncated"
          type="warning"
          :closable="false"
          show-icon
          class="node-truncated-alert"
          data-test="channel-node-truncated"
          :title="nodeTruncatedText"
        />
      </div>
    </div>

    <!-- 错误态：接口失败必须留下常驻痕迹并提供重试入口，
         不得回退成"0 条 / 暂无通道"这一伪造的领域事实（范式同 views/data-source/DataSourceList.vue） -->
    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      show-icon
      class="channel-error-alert"
      data-test="channel-error"
    >
      <template #title>
        <span class="channel-error-text">加载通道列表失败：{{ loadError }}</span>
        <el-button link type="primary" size="small" data-test="channel-retry" @click="retryLoad">重试</el-button>
      </template>
    </el-alert>

    <!-- 加载骨架 -->
    <template v-if="loading && channels.length === 0">
      <div class="skeleton-grid">
        <SkeletonCard v-for="i in 6" :key="i" variant="card" animated />
      </div>
    </template>

    <!-- 错误状态：优先于空态。加载失败不是"没有通道"，不得用空态冒充（§3.4.2）。 -->
    <EmptyState
      v-else-if="loadError"
      kind="error"
      icon="WarningFilled"
      data-test="channel-error-state"
      title="通道列表加载失败"
      description="接口请求失败，暂时无法确定通道数量。"
      :quick-actions="[{ label: '重试', type: 'primary', handler: retryLoad }]"
    />

    <!-- 空状态：三种结论必须分开（不得互相冒充）
           ① 服务端全库确实没有通道（无筛选）    → initial
           ② 服务端筛选后全库无匹配（节点/硬件类型）→ filtered，范围=整库筛选
           ③ 只有关键词、且**本页**无匹配（全库可能还有）→ filtered，范围=本页
         ③ 与 ② 的区别是本页分页 + 客户端关键词的必然结果：关键词不下沉服务端，
         所以"本页没搜到"永远不能写成"全库没有"。 -->
    <EmptyState
      v-else-if="visibleChannels.length === 0 && !loading"
      :kind="hasActiveFilters || Boolean(searchKeyword) ? 'filtered' : 'initial'"
      icon="Connection"
      :title="emptyTitle"
      :description="emptyDescription"
    />

    <!-- 通道表格（移动端可横向滚动，见 theme.css .mobile-table-wrapper） -->
    <div v-else class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
      :data="visibleChannels"
      stripe
      class="channel-table"
      @row-click="goToNodeDetail"
      v-loading="loading"
    >
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column label="节点名称" min-width="140">
        <template #default="{ row }">
          <div class="node-name-cell">
            <el-icon :size="20" :color="getNodeStatus(row.node_id) === 'online' ? 'var(--el-color-success)' : 'var(--el-text-color-secondary)'">
              <Cpu />
            </el-icon>
            <span>{{ getNodeName(row.node_id) }}</span>
          </div>
        </template>
      </el-table-column>
      <!-- 总线类型：英文标签走共享 getHardwareLabel；同一行的 hardware_id 由相邻「硬件ID」列
           承担，故本列不再重复渲染它。
           改前这里是「硬件类型 + 硬件ID + 总线类型」三列：前两列与总线类型列同源
           （都来自 row.hardware_type），却一个走 toUpperCase、一个走页面私有 map，
           于是同一事实在一行里出现两种写法（UART / 串行）。 -->
      <el-table-column label="总线类型" width="110">
        <template #default="{ row }">
          <el-tag :type="getHardwareTagType(row.hardware_type)" size="small" effect="plain">
            {{ getHardwareLabel(row.hardware_type) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="hardware_id" label="硬件ID" width="120" />
      <el-table-column prop="address" label="地址" width="100">
        <template #default="{ row }">
          <span v-if="row.address">{{ row.address }}</span>
          <span v-else class="text-muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="启用状态" width="110" align="center">
        <template #default="{ row }">
          <div class="status-cell">
            <!-- 这里是**只读状态指示**（disabled），但依然需要可访问名：
                 screen reader 对 disabled 开关仍会读出"开关，关/开"，
                 没有名字时用户不知道它在说哪个通道的状态。
                 真实开关在通道详情/编辑里，故文案标明"状态"。 -->
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${asChannel(row).name} 启用状态`"
              size="small"
              disabled
            />
            <span class="status-text" :class="{ off: !row.enabled }">
              {{ row.enabled ? '启用' : '禁用' }}
            </span>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="180" align="center">
        <template #default="{ row }">
          <el-button
            v-if="isScannable(row)"
            type="warning"
            text
            size="small"
            :loading="scanningId === row.id"
            @click.stop="handleScan(row)"
          >
            地址扫描
          </el-button>
          <el-button
            type="primary"
            text
            size="small"
            @click.stop="goToNodeDetail(asChannel(row))"
          >
            查看节点
          </el-button>
        </template>
      </el-table-column>
    </el-table>
    </div>

    <!-- 分页：total 必须用**服务端** total（全库过滤后总条数），不能用本地数组长度。
         v-if 用 total > 0 而非 total > pageSize：否则只有一页时用户看不到"共 N 条"，
         而"共 N 条"必须与实际服务端条数一致（规范 §4.3.4）。 -->
    <div v-if="total > 0" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        :page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next"
        background
        size="small"
        @current-change="handlePageChange"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'
import { Refresh, Filter, Cpu } from '@element-plus/icons-vue'
import type { Channel } from '@/api/channel'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'
import { nodeDisplayName } from '@/utils/nodeDisplay'
import { useChannelList } from './composables/useChannelList'

/**
 * 列表状态与取数已整体搬到 composables/useChannelList.ts（纯搬移，行为不变）。
 *
 * 本文件只留两类东西，边界依据：
 *   1. **模板契约**：图标 / 展示组件 / 共享标签源（getHardwareLabel、getHardwareTagType、
 *      nodeDisplayName）—— 编译模板时需要本地绑定，且 hardwareTag.spec.ts 的源码门禁要求
 *      页面文本里出现共享源调用（不得再写页面私有总线标签 map）；
 *   2. **页面出口**：goToNodeDetail（模板事件与路由契约，ChannelList.spec.ts 的源码断言
 *      要求本文件含该 push 语句）与 el-table 作用域槽的 row 收窄（asChannel）。
 * 分页器留在模板里：listPageConventions.spec.ts 按「路由级页面 + 模板含 el-pagination」
 * 定位本页，:total / current-page 绑定必须与取数状态同处一页。
 */
const router = useRouter()

const {
  // 数据与状态
  channels, loading, scanningId, loadError,
  // 筛选
  searchKeyword, nodeFilter, hardwareTypeFilter,
  nodeOptions, nodeFilterTruncated, nodeTruncatedText,
  hasActiveFilters,
  // 分页
  currentPage, pageSize, total,
  // 派生视图
  visibleChannels, searchScopeHint, emptyTitle, emptyDescription,
  getNodeName, getNodeStatus,
  // 行内动作
  isScannable, handleScan,
  // 取数
  refreshData, retryLoad, handleSearch, handleFilter, handlePageChange,
} = useChannelList()

function goToNodeDetail(row: Channel) {
  router.push({ name: 'NodeDetail', params: { id: row.node_id } })
}

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asChannel = (row: unknown) => row as Channel
</script>

<style scoped>
.channel-page {
  padding: 0;
}

.channel-error-alert {
  border-radius: 8px;
  margin-bottom: 16px;
}

.channel-error-text {
  margin-right: 8px;
}

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  gap: 12px;
}

.toolbar-left {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.search-input,
.filter-select {
  width: 200px;
}

/* 关键词范围词（规范 §4.3）：常驻可见，不能被窄屏挤掉 */
.search-scope-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}

.node-truncated-alert {
  border-radius: 8px;
  margin-bottom: 12px;
}

.status-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
}

.status-text {
  font-size: 12px;
  color: var(--el-color-success);
}

.status-text.off {
  color: var(--el-text-color-secondary);
}

.skeleton-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.channel-table {
  width: 100%;
  cursor: pointer;
}

.channel-table :deep(.el-table__row) {
  cursor: pointer;
}

.channel-table :deep(.el-table__row:hover) {
  background-color: var(--el-fill-color-light);
}

.node-name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

/*
 * 2026-10-21 复核实测：节点名回退成「节点 #30EDA0A9A808」后，该串 148px > 单元格 140px，
 * 在空格处折行后第二行仍 148px → 末尾被裁 8px（信息损失）。
 * 节点 ID 是无空格的十六进制长串，允许在字符间断行才是不丢信息的解法。
 */
.node-name-cell span {
  min-width: 0;
  word-break: break-all;
  line-height: 1.3;
}

.text-muted {
  color: var(--el-text-color-secondary);
}

.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

/* 移动端：筛选区竖排堆叠 */
@media (max-width: 768px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .toolbar-left {
    flex-direction: column;
    align-items: stretch;
  }

  .search-input,
  .filter-select {
    width: 100%;
  }

  .pagination-wrapper {
    justify-content: center;
  }
}
</style>
