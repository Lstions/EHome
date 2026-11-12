<template>
  <div class="device-page">
<template v-if="loading">
      <div class="stats-row">
        <SkeletonCard v-for="i in 4" :key="i" variant="stat" :icon-size="48" animated />
      </div>
      <div class="device-grid">
        <SkeletonCard v-for="i in 8" :key="i" variant="card" animated />
      </div>
    </template>
    <template v-else>
      <!-- 顶部双视图切换（在役 / 历史）：历史设备 = 已删除但数据仍保留的设备 -->
      <div class="scope-tabs" data-test="scope-tabs">
        <el-radio-group v-model="deviceScope" data-test="scope-switch">
          <el-radio-button value="active" data-test="scope-active">在役设备</el-radio-button>
          <el-radio-button value="history" data-test="scope-history">历史设备</el-radio-button>
        </el-radio-group>
        <span class="scope-hint">历史设备 = 已删除但数据仍保留；恢复可重新投入使用，彻底删除会清理数据且不可恢复</span>
      </div>

      <!-- 顶部统计（仅在役视图；移动端 4 卡契约见 MobileStatCardsResponsive.spec） -->
      <div v-if="deviceScope === 'active'" class="stats-row">
        <StatCard label="本页边缘设备" :mobile-label="`边缘设备（${SCOPE_PAGE}）`" icon-color="var(--el-color-primary)" @click="handleStatClick('all')">
          <template #icon><el-icon><Cpu /></el-icon></template>
          <template #value><CountUp :value="stats.total" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页在线" :mobile-label="`在线（${SCOPE_PAGE}）`" icon-color="var(--el-color-success)">
          <template #icon><el-icon><CircleCheck /></el-icon></template>
          <template #value><CountUp :value="stats.online" class="stat-value" /></template>
        </StatCard>

        <!-- 第三张卡承载「非在线」的全部设备：离线/异常 + 等待数据。
            2026-10-03 审查发现：pending 原先只计入总数、两个分项都不含它，
             于是「在线 + 离线/异常 < 总数」，用户本就抱怨数字对不上。
             这里**不新增第 5 张卡**：移动端契约（MobileStatCardsResponsive.spec.ts）
             钉死了本页在 ≤768px 下必须是 4 张卡（grid repeat(4,…)），加卡会破坏布局契约。
             改为让第三张卡 = 离线/异常 + 等待数据，使「在线 + 非在线 = 总数」成立，
             且卡内把等待数据单列出来，用户仍能区分两者。 -->
        <StatCard label="本页离线/异常" :mobile-label="`离线/异常（${SCOPE_PAGE}）`" icon-color="var(--el-color-danger)" @click="handleStatClick('offline')">
          <template #icon><el-icon><CircleClose /></el-icon></template>
          <template #value>
            <CountUp :value="stats.offline + stats.pending" class="stat-value" />
          </template>
          <!-- StatCard 只有 icon/value/suffix 三个插槽（见 components/common/StatCard.vue），
               没有 footer。用 suffix 承载"其中等待数据 N"的说明。 -->
          <template #suffix>
            <span v-if="stats.pending > 0" class="stat-sub">等待数据 {{ stats.pending }}</span>
          </template>
        </StatCard>

        <StatCard :label="`今日采样帧（${SCOPE_GLOBAL}）`" icon-color="var(--el-color-info)" @click="handleStatClick('today')">
          <template #icon><el-icon><DataAnalysis /></el-icon></template>
          <!-- 取不到值时走 UNKNOWN 占位符：CountUp 是数字组件，喂字符串会渲染成 NaN 动画 -->
          <template #value>
            <span v-if="todayDataDisplay.unknown" class="stat-value">{{ todayDataDisplay.value }}</span>
            <CountUp v-else :value="todayDataDisplay.value as number" :decimals="todayDataDisplay.decimals" :suffix="todayDataDisplay.suffix" class="stat-value" />
          </template>
        </StatCard>
      </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索名称/类型..."
          prefix-icon="Search"
          clearable
          class="search-input"
        />
        
        <el-select v-model="typeFilter" placeholder="设备类型" clearable style="min-width: 130px;">
          <template #prefix>
            <el-icon><Grid /></el-icon>
          </template>
          <el-option 
            v-for="type in deviceTypes" 
            :key="type.value" 
            :label="type.label" 
            :value="type.value"
          >
            <span>{{ type.label }}</span>
            <el-icon style="margin-left: 8px;"><component :is="type.icon" /></el-icon>
          </el-option>
        </el-select>
        
        <el-select v-if="deviceScope === 'active'" v-model="statusFilter" placeholder="状态" clearable style="min-width: 90px;">
          <el-option label="在线" value="active" />
          <!-- pending 已是真实后端状态（创建后未收到数据），必须可筛选，
               否则用户看到「等待数据」却找不到对应筛选项。 -->
          <el-option label="等待数据" value="pending" />
          <el-option label="离线" value="offline" />
          <el-option label="警告" value="warning" />
          <el-option label="故障" value="error" />
          <el-option label="已禁用" value="disabled" />
        </el-select>
        
        <el-select v-if="deviceScope === 'active'" v-model="hardwareFilter" placeholder="硬件类型" clearable style="min-width: 120px;">
          <el-option label="UART" value="uart" />
          <el-option label="I2C" value="i2c" />
          <el-option label="SPI" value="spi" />
          <el-option label="GPIO" value="gpio" />
          <el-option label="ADC" value="adc" />
        </el-select>
      </div>
      
      <div class="toolbar-right">
        <el-button-group v-if="deviceScope === 'active'" style="margin-right: 12px;">
          <el-button :type="viewMode === 'card' ? 'primary' : ''" aria-label="卡片视图" @click="viewMode = 'card'">
            <el-icon><Grid /></el-icon>
          </el-button>
          <el-button :type="viewMode === 'table' ? 'primary' : ''" aria-label="表格视图" @click="viewMode = 'table'">
            <el-icon><List /></el-icon>
          </el-button>
        </el-button-group>
        <!-- 卡片视图批量管理开关：开启后卡片出现复选框、点卡片切换选中，批量条出现 -->
        <el-switch
          v-if="deviceScope === 'active' && viewMode === 'card'"
          v-model="batchMode"
          class="batch-mode-switch"
          active-text="批量管理"
          aria-label="批量管理"
          data-test="card-batch-toggle"
        />
        <!-- 恢复已删除设备（契约 §4.2）：换节点/误删后的首选修复入口 ——
             复用同一条 edge_devices 记录，历史数据不搬运也连续。两个视图都可打开。 -->
        <el-button data-test="open-restore" @click="openRestoreDialog">
          <el-icon><RefreshLeft /></el-icon>
          恢复已删除设备
        </el-button>
        <el-button v-if="deviceScope === 'active'" type="primary" @click="showCreateDialog = true">
          <el-icon><Plus /></el-icon>
          创建边缘设备
        </el-button>
      </div>
    </div>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="typeFilter" closable @close="typeFilter = ''">类型：{{ getDeviceTypeLabel(typeFilter) }}</el-tag>
      <el-tag v-if="deviceScope === 'active' && statusFilter" closable @close="statusFilter = ''">状态：{{ statusLabel(statusFilter) }}</el-tag>
      <el-tag v-if="deviceScope === 'active' && hardwareFilter" closable @close="hardwareFilter = ''">总线：{{ getHardwareLabel(hardwareFilter) }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 在役批量操作条（表格勾选 / 卡片「批量管理」模式）：含批量启用/停用/间隔/软删/导出 -->
    <div
      class="batch-bar"
      v-if="deviceScope === 'active' && (selectedDevices?.length || 0) > 0 && (viewMode === 'table' || batchMode)"
      data-test="batch-bar-active"
    >
      <span class="batch-info">已选择 <strong>{{ selectedDevices?.length || 0 }}</strong> 个设备</span>
      <div class="batch-actions">
        <el-button size="small" :disabled="batchSubmitting" data-test="batch-enable" @click="batchEnable">
          <el-icon><Check /></el-icon>启用
        </el-button>
        <el-button size="small" :disabled="batchSubmitting" data-test="batch-disable" @click="batchDisable">
          <el-icon><CircleClose /></el-icon>停用
        </el-button>
        <el-button size="small" :disabled="batchSubmitting" data-test="batch-interval" @click="openBatchIntervalDialog">
          <el-icon><Timer /></el-icon>设置采集间隔
        </el-button>
        <el-button size="small" :disabled="batchSubmitting" @click="selectAllPage">全选本页</el-button>
        <el-button size="small" :disabled="batchSubmitting" @click="clearSelection">取消选择</el-button>
        <el-button type="danger" size="small" :disabled="batchSubmitting" @click="handleBatchDelete">
          <el-icon><Delete /></el-icon>批量删除
        </el-button>
        <el-button type="primary" size="small" :disabled="batchSubmitting" @click="handleBatchExport">
          <el-icon><Download /></el-icon>批量导出CSV
        </el-button>
      </div>
    </div>

    <!-- 历史批量操作条：批量彻底删除（记录+数据，后台清理，不可恢复）+ 导出 -->
    <div
      class="batch-bar"
      v-if="deviceScope === 'history' && (historySelection?.length || 0) > 0"
      data-test="batch-bar-history"
    >
      <span class="batch-info">已选择 <strong>{{ historySelection.length }}</strong> 台历史设备</span>
      <div class="batch-actions">
        <el-button size="small" :disabled="historyDeleting" @click="selectAllPage">全选本页</el-button>
        <el-button size="small" :disabled="historyDeleting" @click="clearSelection">取消选择</el-button>
        <el-button
          type="danger"
          size="small"
          :loading="historyDeleting"
          :disabled="historyDeleting"
          data-test="history-batch-delete"
          @click="confirmDeleteHistory(historySelection)"
        >
          <el-icon><Delete /></el-icon>批量彻底删除
        </el-button>
        <el-button type="primary" size="small" :disabled="historyDeleting" data-test="history-export" @click="handleHistoryExport">
          <el-icon><Download /></el-icon>导出CSV
        </el-button>
      </div>
    </div>

    <!-- 设备表格列表（在役视图） -->
    <el-card v-if="deviceScope === 'active' && viewMode === 'table'" shadow="hover">
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper） -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        :data="filteredDevices"
        stripe
        @selection-change="handleSelectionChange"
        ref="tableRef"
      >
        <el-table-column type="selection" width="50" />
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="名称" min-width="150" show-overflow-tooltip />
        <el-table-column prop="device_type" label="类型" width="120">
          <template #default="{ row }">
            <el-tag size="small">{{ getDeviceTypeLabel(row.device_type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="节点" width="150" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.node?.name || ('#' + row.node_id) }}
          </template>
        </el-table-column>
        <el-table-column label="总线" width="120">
          <template #default="{ row }">
            <code>{{ getDeviceBusLabel(asDevice(row)) }}</code>
          </template>
        </el-table-column>
        <el-table-column prop="status" label="状态" width="90">
          <template #default="{ row }">
            <el-tag 
              :type="statusTagType(row.status)"  
              size="small"
              effect="dark">
              {{ statusLabel(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最新数据" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.last_data" class="table-data-text">{{ formatDeviceData(row.last_data) }}</span>
            <span v-else class="table-data-empty">暂无数据</span>
          </template>
        </el-table-column>
        <el-table-column prop="last_data_time" label="最后采集" width="140">
          <template #default="{ row }">
            {{ formatRelativeTime(row.last_data_time) }}
          </template>
        </el-table-column>
        <!-- 操作列仅图标（原 160px 占 390px 视口的 41%）；2026-10-23 新增
             「合并历史数据」入口后 3 → 4 个图标按钮。
             ⚠ 宽度必须容得下 4 个触控按钮：全局 .touch-target / .el-table .el-button--small
             的 min-width 都是 36px（theme.css），加上 .el-table .cell 左右各 12px padding，
             单行需要 ≥ 4×36 + 24 = 168px。2026-10-25 实测 128px 会把按钮挤成 2 行
             （真实 Chromium 复现），故 128 → 168。 -->
        <el-table-column label="操作" width="168" fixed="right">
          <template #default="{ row }">
            <el-button size="small" class="touch-target" :aria-label="`查看 ${row.name}`" @click="goToDetail(row.id)">
              <el-icon><View /></el-icon>
            </el-button>
            <el-button size="small" class="touch-target" :aria-label="`编辑 ${row.name}`" @click="handleEdit(row)">
              <el-icon><Edit /></el-icon>
            </el-button>
            <!-- 合并历史数据（2026-10-25 产品重做）：以本行为**目标设备**，
                 只能把「同型号 + 已删除」的历史设备数据并过来（旧记录已删、不想恢复时的收尾）。
                 设备还在/只是换位置请用「编辑」；记录被删请先用「恢复已删除设备」。 -->
            <el-button size="small" class="touch-target" :aria-label="`为 ${row.name} 合并历史数据`" data-test="row-merge" @click="openMergeDialog(asDevice(row))">
              <el-icon><FolderAdd /></el-icon>
            </el-button>
            <el-button size="small" type="danger" text class="touch-target" :aria-label="`删除 ${row.name}`" @click="handleDelete(asDevice(row))">
              <el-icon><Delete /></el-icon>
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      </div>
    </el-card>

    <!-- 设备卡片列表 -->
    <!-- （在役视图；卡片「批量管理」模式下点卡片=切换选中） -->
    <div class="device-grid" v-if="deviceScope === 'active' && viewMode === 'card'">
      <el-card 
        v-for="device in filteredDevices" 
        :key="device.id" 
        class="device-card"
        :class="{
          offline: device.status === 'offline' || device.status === 'disabled',
          'is-batch-mode': batchMode,
          'is-batch-selected': batchMode && isCardSelected(device),
        }"
        shadow="hover"
        :role="batchMode ? 'checkbox' : undefined"
        :tabindex="batchMode ? 0 : undefined"
        :aria-checked="batchMode ? isCardSelected(device) : undefined"
        @click="onCardClick(device, $event)"
        @keydown.enter="onCardKeydown(device, $event)"
        @keydown.space.prevent="onCardKeydown(device, $event)"
      >
        <div class="card-header">
          <!-- 卡片批量模式：复选框（点卡片任意空白处也可切换选中） -->
          <el-checkbox
            v-if="batchMode"
            class="card-batch-checkbox"
            :model-value="isCardSelected(device)"
            :aria-label="`选择设备 ${device.name}`"
            data-test="card-batch-checkbox"
            @click.stop
            @change="toggleCardSelection(device)"
          />
          <div class="device-type-icon device-icon" :class="getDeviceTone(device.device_type)">
            <el-icon :size="20"><component :is="getDeviceIcon(device.device_type)" /></el-icon>
          </div>
          <div class="device-info">
            <h3 :title="device.name">{{ device.name }}</h3>
            <div class="device-meta">
              <el-tag size="small" :title="getDeviceTypeLabel(device.device_type)">{{ getDeviceTypeLabel(device.device_type) }}</el-tag>
              <el-tag v-if="device.protocol" size="small" type="info">{{ device.protocol.toUpperCase() }}</el-tag>
            </div>
          </div>
          <div class="status-indicator" :class="device.status">
            <span class="dot"></span>
            {{ statusLabel(device.status) }}
          </div>
        </div>

        <!-- 位置信息做成 chips（2026-10-17 卡片重设计转正）：节点可点击复制；
             从机地址只在**真的有地址参数**时显示 —— UART 点对点没有地址，
             不得拿 hardware_id 冒充（见 addressValue 注释）。 -->
        <div class="card-chips">
          <button
            type="button"
            class="card-chip is-copy"
            :title="`点击复制节点：${device.node?.name || ('#' + device.node_id)}`"
            :aria-label="`复制所属节点 ${device.node?.name || ('#' + device.node_id)}`"
            @click="copyText(device.node?.name || ('#' + device.node_id))"
          >
            <el-icon :size="14"><Connection /></el-icon>
            <span class="chip-label">节点</span>
            <b class="chip-value mono">{{ device.node?.name || ('#' + device.node_id) }}</b>
          </button>
          <span class="card-chip">
            <el-icon :size="14"><DataLine /></el-icon>
            <span class="chip-label">通道</span>
            <b class="chip-value">{{ getDeviceBusLabel(device) }}</b>
          </span>
          <span v-if="addressValue(device)" class="card-chip">
            <el-icon :size="14"><Location /></el-icon>
            <span class="chip-label">从机地址</span>
            <b class="chip-value mono">{{ addressValue(device) }}</b>
          </span>
        </div>

        <!-- 最新读数中文语义化：原先直打后端字段名（cell_count: 16.00 | cell_overvolt...），
             既读不懂也违反「中文优先、不出现内部键名」。现按设备类型挑 3 个关键指标。
             数值格式与标签取自 utils/deviceReadings.ts 单一事实源。 -->
        <div v-if="readingsOf(device).length" class="card-readings">
          <div v-for="r in readingsOf(device)" :key="r.key" class="card-reading">
            <span class="r-label" :title="r.label">{{ r.label }}</span>
            <span class="r-value">{{ r.value }}<span v-if="r.unit" class="r-unit">{{ r.unit }}</span></span>
          </div>
        </div>
        <div v-else class="card-readings is-empty">
          <el-icon :size="15"><Warning /></el-icon>
          <span>等待首条采集数据</span>
        </div>
        <p v-if="device.last_data" class="card-updated">更新于 {{ formatRelativeTime(device.last_data_time) }}</p>

        <div class="card-actions">
          <el-button size="small" type="primary" :icon="View" @click="goToDetail(device.id)">详情</el-button>
          <el-button size="small" text :icon="Edit" @click="handleEdit(device)">编辑</el-button>
          <el-button size="small" text :icon="FolderAdd" data-test="card-merge" @click="openMergeDialog(device)">合并历史</el-button>
          <el-button size="small" type="danger" text :icon="Delete" :aria-label="`删除 ${device.name}`" @click="handleDelete(device)">删除</el-button>
        </div>
      </el-card>
    </div>

    <!-- 空状态（在役视图） -->
    <EmptyState
      v-if="deviceScope === 'active' && !loading && (filteredDevices?.length || 0) === 0"
      icon="Cpu"
      title="暂无边缘设备"
      description='进入节点详情页，点击“创建边缘设备”按钮开始添加'
      :quick-actions="[
        { label: '查看节点', icon: View, type: 'primary', handler: () => router.push('/node') }
      ]"
    />

    <!-- 分页 — total 是后端返回的**过滤后全量**条数 (不是当前页条数),
         分页器据此算总页数; v-if 放宽到 total > 0, 否则只有一页时用户看不到
         "共 N 条" 这个关键信息 (与规范 §4.3.4「共 N 条必须与实际渲染记录一致」配套)。 -->
    <div v-if="deviceScope === 'active' && total > 0" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[12, 24, 48]"
        layout="total, sizes, prev, pager, next"
        @current-change="() => fetchDevices()"
        @size-change="handlePageSizeChange"
      />
    </div>

    <!-- ══ 历史设备视图（已删除但数据仍保留；固定表格，信息密度优先） ══ -->
    <!-- 视图本体在 components/EdgeDeviceHistoryPanel.vue：只吃 props / 抛 emit。
         el-table 的勾选同步走它的 defineExpose（见 selectAllPage / clearAllSelection）。 -->
    <EdgeDeviceHistoryPanel
      v-if="deviceScope === 'history'"
      ref="historyPanelRef"
      :items="historyItems"
      :total="historyTotal"
      :page="historyPage"
      :page-size="historyPageSize"
      :loading="historyLoading"
      :error="historyError"
      :purging-count="historyPurgingCount"
      :deleting="historyDeleting"
      @retry="fetchHistory"
      @page-change="handleHistoryPageChange"
      @selection-change="handleHistorySelectionChange"
      @restore="openRestoreForHistory"
      @delete-row="deleteOneHistory"
      @open-restore="openRestoreDialog"
    />

    <!-- 创建 / 编辑设备对话框：模板与 scoped 样式在 components/EdgeDeviceCreateDialog.vue，
         表单状态 / 地址能力判定 / 提交事务在 composables/useEdgeDeviceCreate.ts。
         这里只传控制器对象（同一批 ref）；子组件是单根 el-dialog，DOM 层级与改前一致。 -->
    <EdgeDeviceCreateDialog :ctl="createDialogCtl" />

    <!-- 单删确认弹窗 (方案 v3.3 §2.1) -->
    <DeviceDeleteDialog
      v-model:visible="showDeleteDialog"
      :device="deletingDevice"
      :submitting="deleteSubmitting"
      @confirm="confirmDelete"
    />

    <!-- 批量删除确认弹窗 (方案 v3.3 §2.2) -->
    <DeviceBatchDeleteDialog
      v-model:visible="showBatchDeleteDialog"
      :devices="selectedDevices"
      :submitting="batchDeleteSubmitting"
      @confirm="confirmBatchDelete"
    />

    <!-- 恢复已删除设备（契约 §4.2 / R0 解法②）；历史行操作可带 initialDeviceId 预展开 -->
    <DeviceRestoreDialog
      v-model:visible="showRestoreDialog"
      :initial-device-id="restoreInitialDeviceId"
      @restored="handleRestored"
    />

    <!-- 合并历史数据（2026-10-25 产品重做：只允许「同型号 + 已删除」的历史设备当源） -->
    <DeviceMergeHistoryDialog
      v-model:visible="showMergeDialog"
      :target="mergeTarget"
      @merged="handleMerged"
    />

    <!-- 批量设置采集间隔（在役批量管理） -->
    <el-dialog
      v-model="batchIntervalDialogVisible"
      title="批量设置采集间隔"
      width="420px"
      align-center
      :close-on-click-modal="false"
      data-test="batch-interval-dialog"
    >
      <p class="batch-interval-hint">将把选中的 {{ selectedDevices.length }} 台设备的采集间隔统一设置为：</p>
      <el-input-number
        v-model="batchIntervalMs"
        :min="0"
        :max="3600000"
        :step="1000"
        controls-position="right"
        style="width: 100%"
        data-test="batch-interval-input"
      />
      <p class="batch-interval-hint">单位毫秒；0 = 不调度；范围 0–3600000。</p>
      <p v-if="batchIntervalError" class="batch-interval-error" data-test="batch-interval-error">{{ batchIntervalError }}</p>
      <template #footer>
        <el-button @click="batchIntervalDialogVisible = false">取消</el-button>
        <el-button type="primary" data-test="batch-interval-confirm" @click="confirmBatchInterval">确定</el-button>
      </template>
    </el-dialog>

    <!-- 批量操作失败明细：部分失败必须逐条展示（#id + 原因），不能只 toast 成功数 -->
    <el-dialog
      v-model="failureDialogVisible"
      :title="failureDialogTitle"
      width="620px"
      align-center
      data-test="batch-failure-dialog"
    >
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
        <el-table :data="failureDetail" size="small" max-height="320" data-test="batch-failure-table">
          <el-table-column prop="id" label="设备 ID" width="100" />
          <el-table-column prop="error" label="失败原因" />
        </el-table>
      </div>
      <template #footer>
        <el-button type="primary" data-test="batch-failure-ack" @click="failureDialogVisible = false">知道了</el-button>
      </template>
    </el-dialog>

    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted } from 'vue'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'

/**
 * F14 统计卡范围词（规范 §4.3 MUST）。
 *   前三张卡（本页边缘设备/本页在线/本页离线）由当前页数据算出，已内嵌「本页」；
 *   本项 `todayData` 来自 /api/v1/overview 的服务端全表计数，与当前页/当前筛选无关，
 *   故标「全局」—— 常量化理由同 Monitor.vue。
 *
 * ⚠ 口径（2026-11-07，L1「值不变不入库」无条件生效后必须标注）：
 *   data_count_today 已从「unified_data 今日**行数**」改为「今日**采样帧数**」
 *   （= COUNT(DISTINCT device_id, timestamp)，后端 handler_overview.go 的 D2）。
 *   两者差异巨大：一行 = 一帧里的一个物理量，BMS 一帧 29 个字段 ⇒ 行数约是帧数的 N 倍。
 *   后果：L1 前该卡显示的是行数（虚高），L1 后若不改口径会从 ~4.2 万掉到 ~1.2 万，
 *   用户会读成「今天的数据少了 80%」。改口径后数字对 L1 天然不敏感
 *   （L1 只减少「一帧里写几个字段」，不减少「有过几帧」），且**必须**在标签上写明「帧」，
 *   否则用户不知道这个数是什么单位。
 */
const SCOPE_GLOBAL = '全局'
/** 前三张卡由当前页数据算出（与桌面 label 里的「本页」同义）。
 *  移动端 ≤768px 时桌面 label 被 display:none 隐藏，只显示 mobile-label ——
 *  所以 mobile-label **也必须带范围词**，否则移动端用户完全看不到范围（F14 在移动端失效）。 */
const SCOPE_PAGE = '本页'
import { useRouter, useRoute } from 'vue-router'
import {
  Cpu, CircleCheck, CircleClose, DataAnalysis, Grid,
  Plus, View, Edit, Delete, Check, Warning, Connection, DataLine,
  List, Download, Location, RefreshLeft, FolderAdd, Timer
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import {
  compactEdgeDeviceList,
  type DeletedEdgeDevice,
  type EdgeDevice,
  type EdgeDeviceListParams,
} from '@/api/edgeDevice'
import client from '@/api/client'
import { findDriverLeaf } from '@/api/driver'
import { pickDeviceReadings } from '@/utils/deviceReadings'
import type { Channel } from '@/api/channel'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'
import StatCard from '@/components/common/StatCard.vue'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import DeviceBatchDeleteDialog from '@/components/device/DeviceBatchDeleteDialog.vue'
import DeviceRestoreDialog from '@/components/device/DeviceRestoreDialog.vue'
import DeviceMergeHistoryDialog from '@/components/device/DeviceMergeHistoryDialog.vue'
import EdgeDeviceHistoryPanel from './components/EdgeDeviceHistoryPanel.vue'
import EdgeDeviceCreateDialog from './components/EdgeDeviceCreateDialog.vue'
import { canRestoreHistory, useEdgeDeviceHistory } from './composables/useEdgeDeviceHistory'
import { useEdgeDeviceBatch } from './composables/useEdgeDeviceBatch'
import { useDeviceListPresentation } from './composables/useDeviceListPresentation'
import { useEdgeDeviceCreate, type EdgeDeviceCreateDialogCtl } from './composables/useEdgeDeviceCreate'
import { useDeviceDriverTree } from './composables/useDeviceDriverTree'
import { deviceTypeOptions } from '@/utils/deviceType'
import { getHardwareDisplay, getHardwareLabel } from '@/utils/hardwareTag'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asDevice = (row: unknown) => row as EdgeDevice
import { useDebouncedSearch } from '@/composables/useDebouncedSearch'
import { useDeviceDelete } from '@/composables/useDeviceDelete'

const router = useRouter()
const route = useRoute()
const edgeDeviceStore = useEdgeDeviceStore()

/**
 * 展示映射（状态/型号/图标 → 中文与样式，时间 → 可读文本）来自
 * composables/useDeviceListPresentation.ts：模板里的 statusLabel(...) / formatRelativeTime(...)
 * 调用形态与搬移前完全一致，页面不再维护第二份映射表。
 */
const {
  getDeviceIcon, getDeviceTone, getDeviceTypeLabel,
  statusTagType, statusLabel, copyText,
  formatRelativeTime, formatDeviceData,
} = useDeviceListPresentation()

// 视图模式：card | table
const viewMode = ref<'card' | 'table'>('card')

// @ts-ignore - table ref used in template
const tableRef = ref()

// 状态
const currentPage = ref(1)
const pageSize = ref(24)
const total = ref(0)
const devices = ref<EdgeDevice[]>([])
const typeFilter = ref('')
const statusFilter = ref('')
const hardwareFilter = ref('')
const showCreateDialog = ref(false)

// ═══════════════════════════════════════════════════════════════════════
// 创建 / 编辑设备向导（2026-11-12 第二轮拆分 + 第三轮子域拆分，纯搬移）
//   模板 + scoped 样式 → components/EdgeDeviceCreateDialog.vue
//   向导状态 / 提交事务 / 编排 → composables/useEdgeDeviceCreate.ts
//   地址能力纯函数   → edgeDeviceAddress.ts
//   驱动树快照       → composables/useDeviceDriverTree.ts
// 页面这一层只做三件事：
//   ① 把页面持有的 showCreateDialog、列表刷新与**同一份驱动树快照**交给组合式（都只有一份）；
//   ② 解构出**仍然必须留在页面 setup 里**的绑定：模板已搬走，但既有门禁 spec 逐字按
//      vm.<name> 驱动向导（vm.selectedParser = ... / vm.handleCreate() ...），
//      解构即保留同名 setupState 入口，且与对话框读到的是同一批 ref；
//   ③ 组一个稳定的 ctl 对象传给对话框子组件（见下方 createDialogCtl）。
// ═══════════════════════════════════════════════════════════════════════

// 驱动树快照：卡片「从机地址」判定与创建向导的地址能力必须看同一份（见 useDeviceDriverTree.ts）。
const { driverTree, fetchDriverTreeOnce, resolveModelRequiresTargetAddress } = useDeviceDriverTree()

const createCtl = useEdgeDeviceCreate({
  showCreateDialog,
  // fetchDevices 在本文件稍后声明：只给惰性适配器，setup 期不会调用它（同 useEdgeDeviceBatch）。
  fetchDevices: (force, throwOnError) => fetchDevices(force, throwOnError),
  // 同一份驱动树快照的取数与判定（不 new 第二份，否则卡片与提交口径会漂移）。
  fetchDriverTreeOnce,
  resolveModelRequiresTargetAddress,
})
const {
  // 页面自身仍在用：行内「编辑」入口
  handleEdit,
  // 只为门禁保留的 setupState 入口（页面文本已不再直接引用，子组件经 ctl 消费）
  createStep,
  selectedParser,
  selectedChannel,
  channelTab,
  deviceForm,
  newChannel,
  deviceFormRef,
  commandIntervalsRef,
  commandIntervalsError,
  hasBusOptions,
  unavailableBusMessage,
  canGoNext,
  availableBusesForType,
  busTypeReported,
  handleCreate,
  handleNodeChange,
  selectParser,
  resetCreateDialog,
  onNewChannelBusTypeChange,
  ...createRest
} = createCtl

const routeSearch = typeof route.query.search === 'string' ? route.query.search : ''
const routeStatus = typeof route.query.status === 'string' ? route.query.status : ''

// ── 服务端分页 + 服务端筛选 (负债 I-11) ──
// 四个筛选 (search/device_type/status/hardware) 全部下沉到 GET /edge-devices,
// 与后端 Count/Find 两侧同口径 —— 检索覆盖全库, 而不是"当前页本地过滤伪装成
// 全局检索" (§3.3.5)。search 走 300ms 防抖; 其余三个是离散选择, 即时下发。
//
// useDebouncedSearch 只借它的**防抖输入**能力: filteredItems 在当前页上做本地
// 过滤毫无用处 (服务端已筛过), 故不解构它, 只取 searchKeyword/debouncedKeyword。
const {
  searchKeyword,
  debouncedKeyword,
  clear: clearSearch,
} = useDebouncedSearch(devices, {
  searchFields: (d) => [d.name || '', d.device_type || '', getDeviceTypeLabel(d.device_type) || ''],
})

const getListParams = (): EdgeDeviceListParams => {
  const params: EdgeDeviceListParams = { page: currentPage.value, page_size: pageSize.value }
  if (debouncedKeyword.value.trim()) params.search = debouncedKeyword.value.trim()
  if (typeFilter.value) params.device_type = typeFilter.value
  if (statusFilter.value) params.status = statusFilter.value
  if (hardwareFilter.value) params.hardware_type = hardwareFilter.value
  return params
}
const initialCache = edgeDeviceStore.getCachedList(getListParams())
const hasInitialCache = !!initialCache
const loading = ref(!hasInitialCache)
devices.value = compactEdgeDeviceList(initialCache?.items)

const hasActiveFilters = computed(() => Boolean(searchKeyword.value || typeFilter.value || statusFilter.value || hardwareFilter.value))

// 路由参数初始化（必须在 useDebouncedSearch 之后）
if (routeSearch) searchKeyword.value = routeSearch
if (['active', 'online', 'offline', 'warning', 'error', 'disabled', 'pending'].includes(routeStatus)) {
  statusFilter.value = routeStatus
}

// 删除管理 — 由 useDeviceDelete composable 封装
const {
  selectedDevices,
  showDeleteDialog,
  deletingDevice,
  deleteSubmitting,
  handleDelete,
  confirmDelete: confirmDeleteBase,
  showBatchDeleteDialog,
  batchDeleteSubmitting,
  handleBatchDelete,
  confirmBatchDelete: confirmBatchDeleteBase,
  handleSelectionChange,
} = useDeviceDelete({
  onSuccess: (deletedIds, _deleteData) => {
    devices.value = devices.value.filter(d => !deletedIds.includes(d.id))
    total.value = Math.max(0, total.value - deletedIds.length)
    updateStats()
    fetchDevices(true).catch(() => {
      ElMessage.warning('删除结果已保存，但列表刷新失败，请稍后刷新')
    })
    // 软删后的设备进入历史视图：历史列表已加载过就同步刷新（未加载则等切换时再取）。
    if (historyLoaded.value) void fetchHistory()
  },
})

// ── 恢复已删除设备 / 合并历史数据（契约 §4.2 / §4.3，R0 的两条落地路径）──
const showRestoreDialog = ref(false)
const showMergeDialog = ref(false)
const mergeTarget = ref<EdgeDevice | null>(null)

const openMergeDialog = (device: EdgeDevice) => {
  mergeTarget.value = device
  showMergeDialog.value = true
}

/** 恢复成功：设备重新出现在存活列表里，必须回后端重取（缓存里没有它）；历史列表同步刷新。 */
const handleRestored = async (_deviceId: number) => {
  edgeDeviceStore.invalidateLists()
  edgeDeviceStore.invalidateDetail(_deviceId)
  restoreInitialDeviceId.value = undefined
  await fetchDevices(true)
  if (historyLoaded.value) await fetchHistory()
}

/**
 * 合并成功（2026-10-25 产品重做后只可能是「同型号已删除源」）：
 *   · 搬了行 ⇒ 目标设备的数据量/最后采集变了，强制重取列表；
 *   · warnings（人工 data_sources 冲突、台账迁移失败等）⇒ 提示用户，列表不必然变
 *     （rows=0 时没有任何数据变化，刷新是无效请求）；
 *   · 产品重做后不再有"停用源设备"概念（在役设备根本不能当源，后端直接 409）。
 */
const handleMerged = async (payload: {
  targetId: number
  rowsMigrated: number
  warnings?: string[]
}) => {
  const warnings = payload.warnings ?? []
  if (warnings.length > 0) {
    ElMessage.warning(`合并完成，但有 ${warnings.length} 条告警，请核对历史数据是否完整（详见合并结果）`)
  }
  if (payload.rowsMigrated > 0) {
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(payload.targetId)
    await fetchDevices(true)
  }
}


// ═══════════════════════════════════════════════════════════════════════
// 在役 / 历史 双视图 + 批量管理（2026-10-25 产品需求）
//   在役：批量启用/停用/设置采集间隔/软删/导出（表格勾选 + 卡片批量模式）
//   历史：已删除但仍保留数据的设备（GET /deleted，固定表格）——
//         可恢复、可彻底删除（记录+数据后台清理，不可恢复）、可批量彻底删除/导出。
//   两种删除语义严格区分：在役「删除」=软删可恢复；历史「彻底删除」=不可恢复。
// ═══════════════════════════════════════════════════════════════════════

/** 当前视图：在役（默认）/ 历史（已删除但数据仍保留）。 */
const deviceScope = ref<'active' | 'history'>('active')

// ═══════════════════════════════════════════════════════════════════════
// 双视图协调层：在役 / 历史共用「批量选择」与「失败明细弹窗」两处状态，
// 所以这两个跨视图动作留在页面；两个 composable 通过回调参数使用它们，
// 而不是各自再持有一份（两份选择集会让「清空」只清一半，是最难查的那类缺陷）。
// ═══════════════════════════════════════════════════════════════════════

/** 历史面板（子组件）暴露的命令式入口：el-table 实例只存在于它内部。 */
const historyPanelRef = ref<{ clearTableSelection: () => void; selectAllRows: () => boolean } | null>(null)

const clearAllSelection = () => {
  selectedDevices.value = []
  historySelection.value = []
  tableRef.value?.clearSelection?.()
  historyPanelRef.value?.clearTableSelection?.()
}

/** 批量执行失败明细（部分失败必须逐条展示，而不是只 toast 成功数）。 */
const failureDialogVisible = ref(false)
const failureDialogTitle = ref('')
const failureDetail = ref<Array<{ id: number; error: string }>>([])

/** 两条批量路径（在役批量操作 / 历史彻底删除）的失败明细统一出口。 */
function reportFailure(title: string, rows: Array<{ id: number; error: string }>) {
  failureDialogTitle.value = title
  failureDetail.value = rows
  failureDialogVisible.value = true
}

// ── 历史设备视图：状态与操作在 composables/useEdgeDeviceHistory.ts，
//    视图在 components/EdgeDeviceHistoryPanel.vue（已删除但数据仍保留的设备）──
const {
  historyItems,
  historyTotal,
  historyPage,
  historyPageSize,
  historyLoading,
  historyLoaded,
  historyError,
  historySelection,
  historyDeleting,
  historyPurgingCount,
  fetchHistory,
  handleHistorySelectionChange,
  handleHistoryPageChange,
  handleHistoryExport,
  confirmDeleteHistory,
  deleteOneHistory,
} = useEdgeDeviceHistory({
  debouncedKeyword,
  typeFilter,
  onClearSelection: clearAllSelection,
  reportFailure,
})

// ── 在役批量管理：状态与操作在 composables/useEdgeDeviceBatch.ts，
//    选择集与 useDeviceDelete 共用同一个 ref（两个入口必须看到同一份选择）──
const {
  batchMode,
  batchSubmitting,
  batchIntervalDialogVisible,
  batchIntervalMs,
  batchIntervalError,
  batchEnable,
  batchDisable,
  openBatchIntervalDialog,
  confirmBatchInterval,
  isCardSelected,
  toggleCardSelection,
  onCardClick,
  onCardKeydown,
} = useEdgeDeviceBatch({
  selectedDevices,
  // fetchDevices 在本文件稍后声明：这里只给惰性适配器，composable 在 setup 期不会调用它。
  fetchDevices: (force, throwOnError) => fetchDevices(force, throwOnError),
  onClearSelection: clearAllSelection,
  reportFailure,
})

/** 历史行「恢复」：打开恢复弹窗并预展开该设备（initialDeviceId）。 */
const restoreInitialDeviceId = ref<number | undefined>(undefined)
const openRestoreDialog = () => {
  restoreInitialDeviceId.value = undefined
  showRestoreDialog.value = true
}
const openRestoreForHistory = (row: DeletedEdgeDevice) => {
  if (!canRestoreHistory(row)) return
  restoreInitialDeviceId.value = row.id
  showRestoreDialog.value = true
}

/** 全选本页 / 取消选择：表格视图走 el-table API（保持其内部勾选状态同步），卡片视图直接改选择集。 */
const selectAllPage = () => {
  if (deviceScope.value === 'history') {
    // 历史表格在子组件内：交给它自己 clearSelection + 逐行勾选；拿不到表格实例时退回直接赋值选择集。
    if (!historyPanelRef.value?.selectAllRows?.()) {
      historySelection.value = [...historyItems.value]
    }
    return
  }
  if (viewMode.value === 'table') {
    const table = tableRef.value
    if (!table) return
    table.clearSelection?.()
    for (const row of devices.value) table.toggleRowSelection?.(row, true)
    return
  }
  selectedDevices.value = [...devices.value]
}
const clearSelection = () => { clearAllSelection() }

// 视图切换：批量选择状态不跨视图残留。
watch(deviceScope, (scope) => {
  batchMode.value = false
  clearAllSelection()
  if (scope === 'history') void fetchHistory()
})
// 卡片批量模式只对卡片视图有意义：切到表格视图时自动退出并清空选择。
watch(viewMode, (mode) => {
  if (mode !== 'card' && batchMode.value) {
    batchMode.value = false
    clearAllSelection()
  }
})
// 关闭「批量管理」开关即清空选择（产品规格 §3.2）。
watch(batchMode, (on) => {
  if (!on) clearAllSelection()
})

// 统计数据
const stats = reactive({
  total: 0,
  online: 0,
  // pending：已创建但尚未收到任何数据（后端 EdgeDeviceStatusPending）。
  // 单列一项，使 total = online + pending + offline 可被用户自行核对。
  pending: 0,
  offline: 0,
  // null = 后端未提供该字段（或请求失败），显示 UNKNOWN；0 是「今天是 0 条」的合法值，
  // 两者必须可区分 —— 用 0 兜底会把「拿不到数据」显示成「没有数据」。
  todayData: null as number | null
})

// 获取今日采样帧统计
//
// F14-b 修复要点（两处都必须对，缺一仍然恒 0）：
//   ① 层级：axios 拦截器返回的是整个 envelope `{code,data,message}`（见 api/client.ts
//      的 `return response.data`），所以 payload 在 `response.data` 上，不是 `response` 上。
//   ② 缺字段不得伪装成 0：字段不存在时显示「—」（UNKNOWN），而不是 `|| 0`。
//      旧写法把「后端没这个字段」和「今天确实 0 条」渲染成同一个 '0'，
//      缺陷因此藏了很久 —— 这正是本次要钉住的行为。
const fetchTodayDataCount = async () => {
  try {
    const response = await client.get<unknown, any>('/api/v1/overview')
    const raw = response.data?.data_count_today
    stats.todayData = typeof raw === 'number' ? raw : null
  } catch {
    stats.todayData = null
  }
}

// 今日**采样帧**展示：<1000 显示原始整数（避免 "0.0k"），>=1000 以千为单位带一位小数。
// ⚠ 单位是「帧」不是「条/行」：口径见上方 SCOPE_GLOBAL 注释块（D2）。
// 取不到值时走 UNKNOWN 占位符（不进 CountUp —— 它是数字组件，喂不了字符串）。
const todayDataDisplay = computed(() => {
  const count = stats.todayData
  if (count === null) return { value: UNKNOWN, decimals: 0, suffix: '', unknown: true }
  return count >= 1000
    ? { value: count / 1000, decimals: 1, suffix: 'k', unknown: false }
    : { value: count, decimals: 0, suffix: '', unknown: false }
})

// 设备类型定义 — 从 deviceType.ts 统一导入
const deviceTypes = deviceTypeOptions

// 表格/卡片直接渲染接口返回的当前页, **不再做本地切片/过滤** (真分页)。
// 改前这里对 devices.value (当时是全量数组) 做本地 filter, 于是"筛选"只在当前页
// 生效、"翻页"只是本地切片 —— 两者都伪装成了全局行为。筛选已下沉服务端。
const filteredDevices = computed(() => devices.value)

// 获取边缘设备列表
let listRequestSequence = 0
const fetchDevices = async (force = false, throwOnError = false) => {
  const sequence = ++listRequestSequence
  const params = getListParams()
  const showInitialSkeleton = !edgeDeviceStore.hasCachedList(params)
  if (showInitialSkeleton) loading.value = true
  try {
    await edgeDeviceStore.fetchList(params, force)
    if (sequence !== listRequestSequence) return
    const cached = edgeDeviceStore.getCachedList(params)
    devices.value = compactEdgeDeviceList(cached?.items)
    total.value = cached?.total || 0
    updateStats()
  } catch (error) {
    if (sequence === listRequestSequence && !throwOnError) feedback.handleError(error, '获取边缘设备列表失败')
    if (throwOnError) throw error
  } finally {
    if (showInitialSkeleton && sequence === listRequestSequence) loading.value = false
  }
}

// 更新统计
const updateStats = () => {
  const allVisible = compactEdgeDeviceList(devices.value)
  stats.total = allVisible.length
  // 只有 active/online 才是「在线」；pending（已创建未采到数据）不算，
  // 否则刚建的空设备会被计入在线，正是缺陷 5 的统计口径版本。
  stats.online = allVisible.filter(d => d.status === 'active' || d.status === 'online').length
  // pending 也不计入「离线/异常」：它在等第一帧数据，offlinedetector 尚未
  // 判定（超时后才转 offline）。算进离线会让新建设备一出现就显示故障。
  //
  // 但**必须单列一张卡**（2026-10-03 审查发现）：此前 pending 只进 total，
  // 两个分项都不含它，于是三张卡并排却「在线 + 离线/异常 < 总数」，
  // 用户本来就在抱怨数字对不上 —— 修一个显示错误不该引入一个新的对不上。
  // 单列后 total = online + pending + offline，三卡可自校验。
  stats.pending = allVisible.filter(d => d.status === 'pending' || d.status === 'initializing').length
  stats.offline = allVisible.filter(d => d.status === 'offline' || d.status === 'disabled' || d.status === 'error' || d.status === 'warning').length
  fetchTodayDataCount()
}

// 设备图标 / 语义色调 / 类型标签、状态标签与标签色、时间与原始数据的可读化，
// 已搬到 composables/useDeviceListPresentation.ts（纯映射、无状态，历史面板与 CSV 导出共用）。
// 这里只留必须读页面状态的取值口径：读数挑选、从机地址、总线列与通道名。

/** 卡片最新读数（中文语义化，按设备类型挑 3 个关键指标）。 */
const readingsOf = (device: any) => pickDeviceReadings(device, 3)

/**
 * 卡片「从机地址」栏的取值。
 *
 * ⚠ 不能见到 hardware_id 就显示：UART 是点对点总线，**没有地址参数**，
 * 该字段在 UART 上只是遗留默认值（1），拿它当地址展示会误导
 * （2026-10-16 评审 demo 实测发现）。
 *
 * 判定用**后端真源**而非字符串形态：/device-configs/tree 的
 * requires_target_address 表示该型号是否真的消费物理地址（Modbus 类为 true）。
 * I2C 器件的 hardware_id 是器件地址（0x44 / 0x76），仍然展示。
 * 其余（UART 点对点 / SPI）一律不展示。
 */
const addressValue = (device: any): string => {
  const hw = device?.hardware_id ? String(device.hardware_id) : ''
  if (!hw) return ''
  const cap = findDriverLeaf(driverTree.value, device.device_type)?.requires_target_address
  if (cap === true) return hw
  const bus = String(device?.channel?.bus_type || device?.hardware_type || '').toLowerCase()
  if (bus === 'i2c') return hw
  return ''
}

/**
 * 「总线」列 / 「总线通道」栏 / CSV「总线」列的唯一取值口径。
 *
 * 为什么不能再拼 `hardware_type + hardware_id`：边缘设备上这两个字段的语义是
 *   · hardware_type —— 设备上为空/无意义；
 *   · hardware_id   —— **Modbus/I2C 从站地址**（雨量计 = "1"、"0x76"），不是总线名。
 * 拼出来就是 "UART 1" 这种把**地址当总线名**的误导值
 * （2026-09-17 生产实测：真实挂在 UART0 的雨量计显示 "UART 1"）。
 *
 * 真正的总线名在**关联通道**上：device.channel_hardware_id（= 后端 channel.hardware_id，
 * "UART0"/"UART1"）；取不到时再读 channel.hardware_id（老后端/局部对象）。
 * 两者都没有（老数据 / 紧凑列表没有 channel）时回退为 UNKNOWN（'—'），
 * **绝不**退回 hardware_type + hardware_id 拼接 —— 那等于把设备地址当总线名展示。
 */
const getDeviceBusLabel = (device: EdgeDevice | null | undefined): string => {
  if (!device) return UNKNOWN
  return device.channel_hardware_id || device.channel?.hardware_id || UNKNOWN
}

/**
 * 通道选择卡上显示的通道名。
 *
 * 用户 2026-10-03 的原话：「一会 uart0，一会又 uart uart0，一会 UART，串行」。
 * 这里原本是 `(hardware_type).toUpperCase() + " " + hardware_id`，
 * 当 hardware_id 本身已含总线名（现场就是 UART0/UART1/I2C0）时就渲染成
 * 「UART UART0」——同一个词重复两遍。
 * 改走共享的 getHardwareDisplay：它按 hardwareIdIncludesBusName 判断，
 * 已含总线名就只显示 hardware_id（UART0），否则补前缀（1 → UART 1）。
 * 全部通道展示都必须走它，不要再在模板里手工拼字符串。
 */
const channelCardLabel = (ch: Channel): string => {
  if (ch.name) return ch.name
  return getHardwareDisplay(ch.hardware_type, ch.hardware_id)
}

/**
 * 传给 EdgeDeviceCreateDialog 的控制器对象。
 * 未被上面解构的字段经 rest 原样透传，解构出来的那批显式回填 —— 两边字段都是**同一批 ref**
 * （不是副本），因此页面 / 门禁 / 对话框始终看到同一份状态。
 * channelCardLabel 留在这里：它是通道卡片名的展示口径（getHardwareDisplay 单一事实源），
 * 属页面展示映射，不塞进表单 / 事务组合式。
 */
const createDialogCtl: EdgeDeviceCreateDialogCtl = {
  ...createRest,
  handleEdit,
  createStep, selectedParser, selectedChannel, channelTab,
  deviceForm, newChannel, deviceFormRef,
  commandIntervalsRef, commandIntervalsError,
  hasBusOptions, unavailableBusMessage, canGoNext,
  availableBusesForType, busTypeReported,
  handleCreate, handleNodeChange, selectParser, resetCreateDialog, onNewChannelBusTypeChange,
  channelCardLabel,
}

const handleStatClick = (status: string) => {
  // 只改 ref: 页码重置 + 重新查询由 watch([...filters]) 统一收口 (§3.2.6 MUST)。
  if (status === 'all') {
    statusFilter.value = ''
  } else if (status === 'offline') {
    statusFilter.value = 'offline'
  } else if (status === 'today') {
    router.push('/data')
  }
}

const goToDetail = (id: number) => {
  router.push(`/edge-device/${id}`)
}

// 删除确认弹窗（ElDialog + radio + 异步数据量估算）
// 由 useDeviceDelete composable 管理状态，此处仅包装刷新逻辑
const confirmDelete = async (deleteData: boolean) => {
  const result = await confirmDeleteBase(deleteData)
  if (result.success && result.id) {
    devices.value = devices.value.filter(item => item?.id !== result.id)
    total.value = Math.max(0, total.value - 1)
    updateStats()
    try {
      await fetchDevices(true, true)
    } catch {
      ElMessage.warning('设备已删除，但列表刷新失败，请稍后刷新')
    }
  }
}

// 批量删除 (方案 v3.3 §2.2: 汇总视图 + 统一 radio)
// 由 useDeviceDelete composable 管理状态，此处仅包装刷新逻辑
const confirmBatchDelete = async (deleteData: boolean) => {
  const result = await confirmBatchDeleteBase(deleteData)
  if (result.success && result.succeededIds) {
    const succeededIds = new Set(result.succeededIds)
    devices.value = devices.value.filter(device => device && !succeededIds.has(device.id))
    total.value = Math.max(0, total.value - result.succeeded)
    updateStats()
    try {
      await fetchDevices(true, true)
    } catch {
      ElMessage.warning('删除结果已保存，但列表刷新失败，请稍后刷新')
    }
  }
}

// 批量导出 CSV
const handleBatchExport = () => {
  if (selectedDevices.value.length === 0) return

  const rows = [['ID', '名称', '类型', '节点', '总线', '状态', '最新数据', '最后采集时间']]
  for (const device of selectedDevices.value) {
    rows.push([
      String(device.id),
      device.name,
      getDeviceTypeLabel(device.device_type),
      device.node?.name || ('#' + device.node_id),
      getDeviceBusLabel(device),
      statusLabel(device.status),
      device.last_data ? formatDeviceData(device.last_data) : '暂无数据',
      formatRelativeTime(device.last_data_time)
    ])
  }

  const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `devices_export_${new Date().toISOString().slice(0, 10)}.csv`
  link.click()
  URL.revokeObjectURL(url)

  ElMessage.success('导出成功')
}

// 筛选变化 → 重置页码后重新查询 (§3.2.6 MUST)。
// 这是"筛选变化"的唯一入口: 模板里的下拉 / 标签关闭 / KPI 钻取都只改 ref,
// 由这里统一收口 —— 避免"某条路径忘了重置页码"或"重复发请求"两种偏差。
// 改前的缺陷: watch 只监听 [typeFilter, statusFilter], hardwareFilter 与
// searchKeyword 变化时页码**不重置**; 且本地过滤会让用户以为在全局筛选。
//
// filterReady 门控挂载期初值: 路由 query 初始化的 search/status 已经在
// getListParams() 首次取数时带上, 不需要再触发一次请求。
let filterReady = false
watch([debouncedKeyword, typeFilter, statusFilter, hardwareFilter], () => {
  if (!filterReady) return
  // 双视图共用搜索/类型筛选：历史视图下同名筛选变化改为刷新历史列表（分页回到第 1 页）。
  if (deviceScope.value === 'history') {
    historyPage.value = 1
    void fetchHistory()
    return
  }
  currentPage.value = 1
  void fetchDevices()
})

// 每页条数变化: 页码回到第 1 页 (原页在新页长下可能已越界), 再查询。
const handlePageSizeChange = () => {
  currentPage.value = 1
  void fetchDevices()
}

// 清空筛选: 只改 ref, 由上面的 watch 统一重置页码 + 重新查询 ——
// 不在这里再调一次 fetchDevices, 否则 watch 会补发第二个请求 (重复取数)。
// useDebouncedSearch 在清空时**立即**同步 debouncedKeyword (不走 300ms 防抖),
// 因此四个 ref 的变化在同一 tick 内被 Vue 批处理成一次 watch 回调。
const clearFilters = () => {
  clearSearch()
  typeFilter.value = ''
  statusFilter.value = ''
  hardwareFilter.value = ''
}

onMounted(() => {
  // 驱动树用于卡片「从机地址」的能力判定（requires_target_address）——
  // 必须在挂载期就拉，不能等用户打开创建向导才拉，否则首屏卡片的地址栏判不出来。
  // fetchDriverTreeOnce 自带幂等守卫，与向导共用同一份快照。
  void fetchDriverTreeOnce()
  fetchDevices().then(() => {
    // 首次加载后再武装筛选 watch, 避免挂载期初值触发一次多余请求。
    filterReady = true
  })
})

</script>

<style scoped>
.device-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

/* ── 在役 / 历史 双视图切换 ── */
.scope-tabs {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.scope-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* ── 卡片批量管理模式 ── */
.batch-mode-switch {
  margin-right: 12px;
}
.device-card.is-batch-mode {
  cursor: pointer;
}
.device-card.is-batch-selected {
  border-color: var(--el-color-primary);
  box-shadow: 0 0 0 2px var(--el-color-primary-light-7);
}
.card-batch-checkbox {
  margin-right: 8px;
  flex: 0 0 auto;
}

/* ── 批量间隔/失败明细 ── */
.batch-interval-hint {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.batch-interval-error {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--el-color-danger);
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
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
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
}

.stat-icon.total { background: var(--brand-gradient); }
.stat-icon.online { background: var(--el-color-success); color: var(--text-on-fill); }
.stat-icon.offline { background: var(--el-text-color-secondary); color: var(--text-on-fill); }
.stat-icon.today { background: var(--el-color-warning); color: var(--text-on-fill); }

.stat-content { flex: 1; }
.stat-value { display: block; font-size: 28px; font-weight: 600; color: var(--el-text-color-primary); }
.stat-label { font-size: 13px; color: var(--el-text-color-secondary); }
/* 「离线/异常」卡内的补充说明（其中等待数据 N）。
   它把 pending 从"看不见"变成"看得见"，同时不新增第 5 张卡
   （移动端契约要求本页 ≤768px 保持 4 张卡）。 */
.stat-sub { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; }


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

.search-input { width: 280px; min-width: 200px; }

/* 设备网格 */
.device-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 20px;
}

/* 卡片在网格里**等高**，且内容纵向 flex —— 两个原因：
   1. 网格行高由最高卡片决定，其余卡片必须撑满，否则底边按钮参差不齐
      （实测：无读数的离线卡片矮一截，"详情"按钮被下边框裁掉）；
   2. .card-actions 的 margin-top:auto 只有在 flex 列容器里才生效，
      否则它是一条**永不生效的规则**（改前就是这样）。 */
.device-card {
  display: flex;
  flex-direction: column;
  /* overflow:hidden 这里必须去掉：它会把"+ 合并历史"撑出的操作行裁掉。
     圆角裁切改由 el-card 自身的 border-radius 负责。 */
  transition: transform 0.2s ease, box-shadow 0.2s ease, border-color 0.2s ease;
  border: 1px solid var(--el-border-color);
}

.device-card:hover {
  transform: translateY(-2px);
  border-color: var(--el-color-primary-light-5);
  box-shadow: var(--shadow-md);
}

.device-card.offline { opacity: 0.78; }

/* 必须是 :deep()：el-card 的 body 在子组件内部，scoped 属性选择器打不进去，
   原写法（无 :deep）实际**从未生效**，body 一直带着默认 20px padding。 */
.device-card :deep(.el-card__body) {
  padding: 0;
  display: flex;
  flex-direction: column;
  flex: 1;
}

.card-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px;
  margin-bottom: 0;
}

.device-info {
  flex: 1;
  min-width: 0;
}

.device-info h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 尺寸/居中/语义色全部交给 theme.css 的 .device-type-icon + .tone-*
   （2026-10-17 全站统一）：本页原来另有一套 .device-icon.wind/rain/light/battery/solar
   与 utils 里的 classMap，是同一件事的第二份实现 —— 改图标或改色时两边会漂移。
   本页只覆盖尺寸：列表卡用 40px。 */
.device-icon { --device-icon-size: 40px; }

.device-meta { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 6px; }

.status-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  white-space: nowrap;
}

.status-indicator.online { background: var(--el-color-success-light-9); color: var(--el-color-success); }
.status-indicator.active { background: var(--el-color-success-light-9); color: var(--el-color-success); }
.status-indicator.offline { background: var(--el-fill-color-light); color: var(--el-text-color-secondary); }
.status-indicator.warning { background: var(--el-color-warning-light-9); color: var(--el-color-warning); }
.status-indicator.error { background: var(--el-color-danger-light-9); color: var(--el-color-danger); }
.status-indicator.disabled { background: var(--el-fill-color-light); color: var(--el-text-color-secondary); }

.status-indicator .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-indicator.online .dot { animation: pulse 2s infinite; }
.status-indicator.active .dot { animation: pulse 2s infinite; }

/* ── 卡片内容（2026-10-17 由评审 demo 转正）────────────────────────────
   原文案：latest data 直打后端字段名（cell_count: 16.00 | cell_overvolt...），
   既读不懂也违反「中文优先、不出现内部键名」。现为位置 chips + 中文读数块。 */
.card-chips { display: flex; flex-wrap: wrap; gap: 8px; padding: 0 16px 12px; }
.card-chip {
  display: inline-flex; align-items: center; gap: 6px; min-width: 0;
  padding: 6px 10px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
  font-size: 12px; color: var(--el-text-color-secondary);
}
button.card-chip { border: none; font-family: inherit; cursor: pointer; }
button.card-chip:hover { background: var(--el-fill-color-light); color: var(--el-color-primary); }
.chip-label { white-space: nowrap; }
.chip-value { color: var(--el-text-color-primary); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 190px; }
.chip-value.mono { font-family: 'SF Mono', Consolas, Monaco, monospace; font-weight: 500; }

/* 3 个关键读数块：灰底、大数值、tabular-nums 对齐 */
.card-readings { display: flex; gap: 8px; padding: 0 16px 12px; }
.card-reading {
  flex: 1 1 0; min-width: 0;
  display: flex; flex-direction: column; gap: 2px;
  padding: 8px 10px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
}
.r-label { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.r-value { font-size: 16px; font-weight: 600; color: var(--el-text-color-primary); font-variant-numeric: tabular-nums; white-space: nowrap; }
.r-unit { margin-left: 2px; font-size: 12px; font-weight: 400; color: var(--el-text-color-secondary); }
.card-readings.is-empty {
  align-items: center; justify-content: center; gap: 6px;
  padding: 14px; margin: 0 16px 12px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter); color: var(--el-text-color-secondary); font-size: 13px;
}
.card-updated { margin: 0; padding: 0 16px 12px; font-size: 12px; color: var(--el-text-color-secondary); }

/* 操作区固定贴底：卡片在网格里等高（需 .device-card 是 flex 列容器），底边按钮对齐。
 *
 * 2026-10-24：加「合并历史」后变成 4 个按钮，卡片最小宽 320px 时**放不下**
 * （详情 ~72px + 编辑 ~64px + 合并历史 ~88px + 删除 ~64px + 间距 ≈ 300px，
 * 再算 32px 左右 padding 就溢出）。原来靠 .device-card 的 overflow:hidden 掩盖，
 * 表现是最后一个按钮被裁一半。修法：
 *   1. 允许换行（flex-wrap），窄卡自动折成两行，不裁任何按钮；
 *   2. 收窄内边距与间距，尽量保持单行；
 *   3. min-width:0 让按钮可被压缩，避免撑破容器。 */
.card-actions {
  margin-top: auto;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 2px 4px;
  padding: 8px 12px;
  border-top: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
}

/* 操作按钮字号略收（12px），4 个按钮在 320px 卡上大概率仍能单行。
   图标按钮本身有 32px 触控区（全局 .touch-target），不因字号变小而缩小热区。 */
.card-actions :deep(.el-button) {
  font-size: 12px;
  padding-left: 6px;
  padding-right: 6px;
  min-width: 0;
}

.card-actions :deep(.el-button > span) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 分页 */
.pagination-wrapper {
  display: flex;
  justify-content: center;
  padding: 20px 0;
}

/* 创建 / 编辑设备对话框的 scoped 样式随模板搬进 components/EdgeDeviceCreateDialog.vue：
   父组件的 scoped 属性选择器打不进子组件内部 DOM，样式必须跟着片段走。 */
/* 批量操作栏 */
.batch-bar {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  background: var(--el-color-primary-light-9);
  border: 1px solid var(--el-color-primary-light-5);
  border-radius: 8px;
  padding: 10px 16px;
}

.batch-info {
  font-size: 14px;
  color: var(--el-text-color-primary);
}

.batch-info strong {
  color: var(--el-color-primary);
}

.batch-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

/* 表格数据样式 */
.table-data-text {
  font-size: 12px;
  color: var(--el-text-color-regular);
}

.table-data-empty {
  font-size: 12px;
  color: var(--el-text-color-placeholder);
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

/* 响应式：中屏 2 列，移动端单行 4 列紧凑横排（占高约一行，让位给列表） */
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
  .toolbar { flex-direction: column; gap: 12px; }
  .toolbar-left { width: 100%; flex-wrap: wrap; }
  .search-input { width: 100%; }
  /* 设备名移动端允许两行，放宽截断 */
  .device-info h3 {
    white-space: normal;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }
}

</style>