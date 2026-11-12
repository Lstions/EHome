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

    <!-- 创建边缘设备对话框 -->
    <el-dialog
      v-model="showCreateDialog"
      :title="editingDeviceId ? '编辑边缘设备' : '创建边缘设备'"
      width="760px"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitting"
      :show-close="!submitting"
      :before-close="handleCreateDialogClose"
      class="create-device-dialog"
    >
      <!-- 步骤指示器(仅创建模式;编辑模式只改基本信息,不走向导) -->
      <el-steps v-if="!editingDeviceId" :active="createStep" finish-status="success" simple style="margin-bottom: 24px;">
        <el-step title="选择设备型号" />
        <el-step title="选择节点 & 通道" />
        <el-step title="基本信息" />
      </el-steps>

      <!-- Step 0: 选择设备型号(仅创建模式)
           （2026-10-23 逻辑设备移除：原「历史数据继承」步骤整体删除 ——
            承接历史数据改由设备页的「恢复已删除设备」「合并历史数据」两个
            显式入口完成，创建流程不再做身份选择。） -->
      <div v-show="!editingDeviceId && createStep === 0">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>选择此设备使用的<strong>设备型号</strong>，设备型号决定了原始字节到物理量的解析方式</span>
        </div>

        <!-- 型号选择步（模板快捷选择 + 解析器卡片）在 components/EdgeDeviceCreateModelStep.vue：
             它是向导里唯一不碰表单状态的一步；步骤外壳（v-show + .step-tip）留在页面，
             因为三步共用同一套骨架样式。 -->
        <EdgeDeviceCreateModelStep
          v-model:selected-template-id="selectedTemplateId"
          :templates="availableTemplates"
          :selected-template="selectedTemplate"
          :parsers="availableParsers"
          :selected-parser="selectedParser"
          @template-change="onTemplateSelected"
          @select-parser="selectParser"
        />
      </div>
      
      <!-- Step 1: 选择/创建通道(仅创建模式) -->
      <div v-show="!editingDeviceId && createStep === 1">
        <el-form :model="deviceForm" :rules="deviceRules">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>选择数据传输的<strong>通道</strong>，或为此设备创建新通道</span>
        </div>
        
        <!-- 已选解析器显示 -->
        <div class="selected-parser-badge" v-if="selectedParser">
          <span>解析器: <strong>{{ selectedParser.name }}</strong></span>
          <el-tag size="small" style="margin-left: 8px;">{{ selectedParser.id }}</el-tag>
        </div>
        
        <!-- 节点选择（Step 2） -->
        <el-form-item label="所属节点" prop="node_id" style="margin-bottom: 16px;">
          <el-select v-model="deviceForm.node_id" placeholder="选择节点" style="width: 100%;" @change="handleNodeChange">
            <el-option 
              v-for="c in onlineCollectors" 
              :key="c.id" 
              :label="`${c.name} (${c.model || '未知型号'})`" 
              :value="c.node_id"
            />
          </el-select>
        </el-form-item>
        
        <!-- 通道 Tabs: 选择已有 / 创建新 -->
        <el-tabs v-model="channelTab" style="margin-top: 20px;">
          <el-tab-pane label="选择已有通道" name="existing">
            <div v-if="existingChannels?.length > 0" class="channel-list">
              <!-- I-9 A 类：通道选择是向导第 2 步的唯一入口（同 .parser-select-card 的问题）。
                   aria-label 用与可见文本一致的回退名，保证读屏能区分多个通道。 -->
              <div
                v-for="ch in existingChannels"
                :key="ch.id"
                class="channel-select-card"
                :class="{ selected: selectedChannel?.id === ch.id }"
                role="button"
                tabindex="0"
                :aria-label="`选择通道 ${channelCardLabel(ch)}`"
                :aria-pressed="selectedChannel?.id === ch.id"
                @click="selectChannel(ch)"
                @keydown.enter.prevent="selectChannel(ch)"
                @keydown.space.prevent="selectChannel(ch)"
              >
                <span class="channel-name" :title="channelCardLabel(ch)">{{ channelCardLabel(ch) }}</span>
                <el-tag size="small" :type="getHardwareTagType(ch.hardware_type)">{{ getHardwareLabel(ch.hardware_type) }}</el-tag>
                <span class="channel-bus-id">{{ ch.hardware_id }}</span>
                <div v-if="selectedChannel?.id === ch.id" class="channel-selected-badge">
                  <el-icon><Check /></el-icon>
                </div>
              </div>
            </div>
            <el-empty v-else description="暂无匹配的通道，请创建新通道" />
          </el-tab-pane>
          
          <el-tab-pane label="创建新通道" name="create">
            <el-form ref="channelFormRef" :model="newChannel" label-position="top" style="margin-top: 12px;">
              <el-row :gutter="16">
                <el-col :span="12">
                  <el-form-item label="硬件类型" prop="hardware_type">
                    <el-select v-model="newChannel.hardware_type" style="width: 100%;" @change="onNewChannelBusTypeChange">
                      <el-option v-for="bus in selectedParserBusTypes" :key="bus" :label="getHardwareLabel(bus)" :value="bus" />
                    </el-select>
                  </el-form-item>
                </el-col>
                <el-col :span="12">
                  <el-form-item label="硬件ID" prop="hardware_id">
                    <!-- D6：选项来自**设备上报的能力**（nodes.capabilities.buses[type] 的 resource.id，
                         0 基，与设备一致）；未上报时不再列出硬编码的虚假资源。 -->
                    <el-select v-model="newChannel.hardware_id" style="width: 100%;" :placeholder="hasBusOptions ? '请选择' : '无可用资源'" :disabled="!hasBusOptions">
                      <el-option v-for="bus in availableBusesForType(newChannel.hardware_type)" :key="bus" :label="bus" :value="bus" />
                    </el-select>
                    <!-- D6(b)：未上报 ≠ 没有该总线。必须明确告知，而不是给一个点不动的空控件。 -->
                    <div v-if="!hasBusOptions" class="bus-unavailable-hint" data-bus-unavailable>
                      <template v-if="capabilitiesLoading">正在读取设备资源上报…</template>
                      <!-- 「未上报该总线」与「上报了但一条资源都没有」是两件事，文案必须分开 -->
                      <template v-else-if="busTypeReported(newChannel.hardware_type)">
                        该节点上报的 {{ getHardwareLabel(newChannel.hardware_type) }} 资源为空，无法创建该总线的通道
                      </template>
                      <template v-else>{{ unavailableBusMessage }}</template>
                    </div>
                  </el-form-item>
                </el-col>
              </el-row>
              
              <!-- I2C 需要地址 -->
              <el-form-item v-if="newChannel.hardware_type === 'i2c'" label="从机地址" prop="address">
                <el-input v-model="newChannel.address" placeholder="如: 0x40" />
              </el-form-item>
              
              <!-- SPI 需要 CS -->
              <el-form-item v-if="newChannel.hardware_type === 'spi'" label="CS 引脚" prop="spi_cs">
                <el-input-number v-model="newChannel.spi_cs" :min="0" :max="48" />
              </el-form-item>
              
              <el-form-item label="采集间隔 (ms)">
                <el-input-number v-model="newChannel.interval_ms" :min="100" :max="3600000" :step="100" />
              </el-form-item>
            </el-form>
          </el-tab-pane>
        </el-tabs>
        </el-form>
      </div>
      
      <!-- Step 2: 基本信息(创建模式 createStep===2;编辑模式直接显示) -->
      <div v-show="editingDeviceId || createStep === 2">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>{{ editingDeviceId ? '修改设备基本信息与绑定位置（换到别的节点/通道后，历史数据仍然连续可查）' : '填写设备基本信息并确认配置' }}</span>
        </div>

        <el-form ref="deviceFormRef" :model="deviceForm" :rules="deviceRules" label-position="top">
          <el-form-item label="边缘设备名称" prop="name">
            <el-input v-model="deviceForm.name" placeholder="请输入边缘设备名称" />
          </el-form-item>

          <!--
            编辑模式「换节点 / 换通道」（契约 §4.1，R0 主路径）

            为什么必须开放：产品负责人真正要的能力是「同一物理设备从节点 A 换到
            节点 B 后历史数据不丢」。后端 PUT /edge-devices/:id 本来就接受
            node_id/channel_id，且**不改 edge_devices.id** ⇒ unified_data.device_id
            不变 ⇒ 历史天然连续。改前编辑框刻意隐藏这两项（文案还写着
            "通道绑定不可在编辑中变更"），于是这条唯一不搬数据的路径在 UI 上不可达，
            用户只能新建一台设备 —— 历史就这样"断"了。

            约束（后端校验，前端同样强制）：新通道必须属于新节点。因此
            「所属节点」一变更就**清空已选通道**并要求重选，避免提交一个
            (node B, channelA) 的非法组合直接吃 400。
          -->
          <template v-if="editingDeviceId">
            <el-form-item label="所属节点" prop="node_id">
              <el-select
                v-model="deviceForm.node_id"
                placeholder="选择节点"
                style="width: 100%"
                data-test="edit-node"
                @change="handleEditNodeChange"
              >
                <el-option
                  v-for="c in collectors"
                  :key="c.id"
                  :label="`${c.name} (${c.model || '未知型号'})`"
                  :value="c.node_id"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="通道" prop="channel_id">
              <el-select
                v-model="deviceForm.channel_id"
                placeholder="选择该节点下的通道"
                style="width: 100%"
                :loading="editChannelsLoading"
                data-test="edit-channel"
              >
                <el-option
                  v-for="ch in editChannels"
                  :key="ch.id"
                  :label="`${channelCardLabel(ch)} · ${getHardwareLabel(ch.hardware_type)}`"
                  :value="ch.id!"
                />
              </el-select>
              <div v-if="editChannels.length === 0 && !editChannelsLoading" class="bus-unavailable-hint">
                该节点下没有可用于此设备型号的通道，请先到通道管理创建
              </div>
            </el-form-item>
          </template>

          <!-- EDGE-WIZ-004/005: 驱动声明 schedulable 轮询指令时, 以逐指令间隔为
               主配置; 全局 interval_ms 仅作后端兼容字段, 不再展示以避免误导。
               无 schedulable 指令的驱动保持原有单个采集间隔。 -->
          <el-form-item v-if="!hasSchedulableCommands" label="采集间隔 (ms)" prop="interval_ms">
            <el-input-number v-model="deviceForm.interval_ms" :min="100" :max="3600000" :step="100" />
          </el-form-item>
          <el-alert v-else :closable="false" type="info" show-icon style="margin-bottom: 16px;">
            <template #title>该设备型号按下方“轮询指令”逐条设置间隔（0 = 禁用）</template>
          </el-alert>
        </el-form>

        <!-- EDGE-WIZ-004/005: 逐指令轮询间隔 (仅创建模式) — 驱动声明了
             schedulable 指令时展示, 替代/补充全局采集间隔的误导。 -->
        <CreateWizardCommandIntervals
          v-if="!editingDeviceId && selectedParser?.id"
          ref="commandIntervalsRef"
          :device-type="selectedParser.id"
          :saving-disabled="submitting"
          @load-error="handleDriverCommandsLoadError"
        />

        <!-- 配置确认卡片(仅创建模式;编辑模式无解析器/通道选择上下文) -->
        <div class="confirm-card" v-if="!editingDeviceId">
          <h4>配置确认</h4>
          <div class="confirm-items">
            <div class="confirm-item">
              <span class="label">解析器</span>
              <span class="value">
                <el-tag type="success">{{ selectedParser?.name }}</el-tag>
                <code class="parser-id">{{ selectedParser?.id }}</code>
              </span>
            </div>
            <div class="confirm-item">
              <span class="label">通道</span>
              <span class="value">
                <code>{{ selectedChannel?.name || generatedChannelName }}</code>
                <el-tag size="small">{{ getHardwareLabel(selectedChannel?.hardware_type || newChannel.hardware_type) }}</el-tag>
              </span>
            </div>
            <div class="confirm-item">
              <span class="label">总线</span>
              <span class="value">{{ selectedChannel?.hardware_id || newChannel.hardware_id }}</span>
            </div>
            <div v-if="commandIntervalsSnapshot && Object.keys(commandIntervalsSnapshot).length > 0" class="confirm-item">
              <span class="label">轮询指令</span>
              <span class="value">{{ Object.keys(commandIntervalsSnapshot).length }} 条已配置</span>
            </div>
            <div v-else class="confirm-item">
              <span class="label">采集间隔</span>
              <span class="value">{{ deviceForm.interval_ms }}ms</span>
            </div>
          </div>
        </div>
      </div>

      <template #footer>
        <template v-if="!editingDeviceId">
          <el-button v-if="createStep > 0" @click="createStep--">上一步</el-button>
          <el-button v-if="createStep < 2" type="primary" :disabled="!canGoNext" @click="createStep++">
            下一步
          </el-button>
          <el-button v-if="createStep === 2" type="primary" :loading="submitting" @click="handleCreate">
            创建边缘设备
          </el-button>
        </template>
        <el-button v-else type="primary" :loading="submitting" @click="handleCreate">
          保存修改
        </el-button>
      </template>
    </el-dialog>

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
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
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
  Plus, View, Edit, Delete, InfoFilled, Check, Warning, Connection, DataLine,
  List, Download, Location, RefreshLeft, FolderAdd, Timer
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useNodeStore } from '@/stores/node'
import { useChannelStore } from '@/stores/channel'
import { useParserStore } from '@/stores/parser'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import {
  compactEdgeDeviceList,
  edgeDeviceApi,
  type DeletedEdgeDevice,
  type EdgeDevice,
  type EdgeDeviceListParams,
} from '@/api/edgeDevice'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import { nodeApi } from '@/api/node'
import client from '@/api/client'
import { logger } from '@/utils/logger'
import { findDriverLeaf, getDriverTree, type DriverTreeNode } from '@/api/driver'
import { pickDeviceReadings } from '@/utils/deviceReadings'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'
import StatCard from '@/components/common/StatCard.vue'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import DeviceBatchDeleteDialog from '@/components/device/DeviceBatchDeleteDialog.vue'
import DeviceRestoreDialog from '@/components/device/DeviceRestoreDialog.vue'
import DeviceMergeHistoryDialog from '@/components/device/DeviceMergeHistoryDialog.vue'
import CreateWizardCommandIntervals from '@/components/device/CreateWizardCommandIntervals.vue'
import EdgeDeviceHistoryPanel from './components/EdgeDeviceHistoryPanel.vue'
import EdgeDeviceCreateModelStep from './components/EdgeDeviceCreateModelStep.vue'
import { canRestoreHistory, useEdgeDeviceHistory } from './composables/useEdgeDeviceHistory'
import { useEdgeDeviceBatch } from './composables/useEdgeDeviceBatch'
import { useDeviceListPresentation } from './composables/useDeviceListPresentation'
import { deviceTypeOptions } from '@/utils/deviceType'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { DEVICE_ADDRESS_DEFAULT, isValidDeviceAddress, parseDeviceAddress } from '@/utils/deviceAddress'
import { getHardwareDisplay, getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asDevice = (row: unknown) => row as EdgeDevice
import { useDebouncedSearch } from '@/composables/useDebouncedSearch'
import { useDeviceDelete } from '@/composables/useDeviceDelete'

const router = useRouter()
const route = useRoute()
const nodeStore = useNodeStore()
const channelStore = useChannelStore()
const parserStore = useParserStore()
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

// ── m-1（2026-09-22）：型号「是否消费设备地址」的能力来源 ─────────────
// 唯一数据源就是 /device-configs/tree 的驱动叶子字段 requires_target_address
// （与后端地址门禁 driverRequiresTargetAddress 同一口径）。这里**不新造**第二个
// 数据源，也不在本文件重写一份判定表：Tree → 型号 → 能力，全程同一张快照。
//
// 为什么能力必须来自后端而不是"字符串形态"：后端只对**真的解析地址**的型号
// （纯 Modbus 类）校验 1-254；纯 I2C/SPI 采集型号原样接受任何标识。按形态判断
// 就会给后者凭空写一个它根本不用的地址 1 —— 正是本次要修的口径不一致。
const driverTree = ref<DriverTreeNode[]>([])
const driverTreeLoaded = ref(false)

/**
 * 该型号是否消费设备地址。三分支（**必须**保持可区分，不能压成布尔）：
 *   true      → 要求地址；
 *   false     → 明确不要求；
 *   undefined → 拿不到能力字段（老后端 / 树未加载完 / 该型号只在 DB 模板里）
 *               ⇒ 调用方退回"字段缺失"的兼容行为。
 *
 * findDriverLeaf 是 @/api/driver 暴露的唯一查询入口，避免各处 find 出不同口径。
 */
const resolveModelRequiresTargetAddress = (deviceType: string | undefined): boolean | undefined => {
  const leaf = findDriverLeaf(driverTree.value, String(deviceType ?? '').trim())
  return typeof leaf?.requires_target_address === 'boolean' ? leaf.requires_target_address : undefined
}

// 视图模式：card | table
const viewMode = ref<'card' | 'table'>('card')

// @ts-ignore - table ref used in template
const tableRef = ref()

// 状态
const collectors = ref<any[]>([])
const currentPage = ref(1)
const pageSize = ref(24)
const total = ref(0)
const devices = ref<EdgeDevice[]>([])
const typeFilter = ref('')
const statusFilter = ref('')
const hardwareFilter = ref('')
const showCreateDialog = ref(false)
const submitting = ref(false)
let createTransactionGeneration = 0

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

// 向导状态
const createStep = ref(0)
const selectedParser = ref<Parser | null>(null)
const selectedChannel = ref<Channel | null>(null)
const channelTab = ref('existing')

// 模板选择 (Step 0 可选)
const availableTemplates = ref<DeviceConfig[]>([])
const selectedTemplateId = ref<number | null>(null)
const selectedTemplate = computed(() =>
  availableTemplates.value.find((t) => t.id === selectedTemplateId.value) || null,
)

// 加载配置模板 (Step 0)
const loadTemplates = async () => {
  try {
    const res = await deviceConfigApi.getList({ page_size: 100 })
    availableTemplates.value = res.items || []
  } catch (error) {
    availableTemplates.value = []
    throw error
  }
}

// 选模板后: 预填表单 + 尝试自动选解析器
const onTemplateSelected = async (templateId: number | undefined | null) => {
  if (!templateId) {
    return
  }
  const tpl = availableTemplates.value.find((t) => t.id === templateId)
  if (!tpl) return
  // 预填设备类型 / 硬件类型 / 协议
  deviceForm.device_type = tpl.device_type
  deviceForm.hardware_type = (tpl.hardware_type as any) || 'uart'
  deviceForm.protocol = (tpl.protocol as any) || 'modbus'
  // 尝试自动选解析器 (用 parser_id 匹配)
  if (tpl.parser_id && availableParsers.value?.length > 0) {
    const matched = availableParsers.value.find(
      (p: Parser) => p.id === tpl.parser_id || p.id?.endsWith(tpl.parser_id || ''),
    )
    if (matched) {
      selectParser(matched)
    }
  }
}

// 编辑边缘设备ID
const editingDeviceId = ref<number | null>(null)
// G4 编辑-保存回路：记录"打开编辑框那一刻"的设备地址值。
// 保存时只在**值真的变了**才把 hardware_id 发出去 —— 编辑表单当前没有任何
// 地址输入项（见步骤 3 表单：只有名称/采集间隔），所以该值永远不会变，
// 保存就不会产生任何地址写入（正是契约要求的「打开编辑框保存不应产生写入」）。
// 若将来给编辑表单加了地址输入框，这里的比较会自动变成"改了才写"，无需再改保存逻辑。
const editingDeviceAddressSnapshot = ref('')

// 设备表单
const deviceForm = reactive({
  node_id: null as number | null,
  // 编辑模式的通道绑定（契约 §4.1：与 node_id 一起提交，后端校验归属关系）。
  // 创建模式仍走向导的 selectedChannel/newChannel，不用这个字段。
  channel_id: null as number | null,
  name: '',
  device_type: '',
  protocol: 'modbus',
  hardware_type: 'uart',
  hardware_id: '',
  interval_ms: 1000
})

const deviceRules = {
  name: [{ required: true, message: '请输入边缘设备名称', trigger: 'blur' }],
  // S10 fix: Add node_id required rule so the red asterisk shows on the form item
  node_id: [{ required: true, message: '请选择所属节点', trigger: 'change' }],
  // 编辑模式必须同时给出通道：后端要求「新 channel 属于新 node」，
  // 只带 node_id 会被 400 拒绝（channel does not belong to node）。
  channel_id: [{ required: true, message: '请选择通道', trigger: 'change' }],
}

// 新通道表单
const newChannel = reactive({
  hardware_type: 'i2c',
  hardware_id: '',
  address: '',
  spi_cs: 10,
  interval_ms: 1000
})

// @ts-ignore - form ref used in template
const channelFormRef = ref()
const deviceFormRef = ref()

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

// 只显示在线节点
const onlineCollectors = computed(() => collectors.value.filter((c: any) => c.status === 'online'))

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

// 获取节点列表
const fetchNodes = async () => {
  const params = { page: 1, page_size: 100 }
  await nodeStore.fetchNodes(params)
  collectors.value = nodeStore.getCachedList(params)?.items || []
}

// EDGE-WIZ-003: 打开创建向导时必须强制刷新通道列表。旧缓存 (节点页刚创建通道
// 后不刷新浏览器) 会导致新建通道不进列表。通道 store 的 fetchChannels 自带
// requestSequence/cacheEpoch 防竞态, 不破坏其 sessionGeneration 机制。
let wizardDataLoaded = false
let channelsRefreshing = false
let nodesFetched = false
const refreshWizardChannels = async () => {
  channelsRefreshing = true
  try {
    await channelStore.fetchChannels(undefined, true)
  } finally {
    channelsRefreshing = false
  }
}
/**
 * 拉取一次驱动树快照（m-1）。失败时保持 driverTreeLoaded=false，并在下次打开
 * 向导时重试 —— 不缓存"失败"这种状态，否则一次网络抖动会让能力查询在整个
 * 页面生命周期内都拿不到值。
 */
const fetchDriverTreeOnce = async () => {
  if (driverTreeLoaded.value) return
  try {
    driverTree.value = await getDriverTree()
    driverTreeLoaded.value = true
  } catch (error) {
    logger.warn('获取驱动树失败，型号地址能力未知，退回默认地址兜底', { error: String(error) })
  }
}

const loadCreateWizardData = async () => {
  if (wizardDataLoaded) {
    // 向导已初始化过: 树上一次没拿到（driverTreeLoaded=false）也要补一次，
    // 否则用户第二次打开向导时仍然拿不到能力字段。
    void fetchDriverTreeOnce()
    // 向导已初始化过: 节点/型号/模板不再重复拉取, 但通道列表每次打开都强制
    // 刷新 — 节点页刚创建的通道必须立即可见。并发守卫在 store 内
    // (requestSequence), 此处只需防重复请求压栈。
    void refreshWizardChannels().catch(() => {
      // 失败保留已有列表, 用户可通过切换节点或重开向导重试
    })
    return
  }
  try {
    await Promise.all([
      (async () => {
        // 节点列表只需要拉取一次即可; 通道列表每次打开向导都强制刷新
        if (nodesFetched) return
        await fetchNodes()
        nodesFetched = true
      })(),
      refreshWizardChannels(),
      parserStore.parsers.length === 0 ? parserStore.fetchParsers(true) : Promise.resolve(),
      loadTemplates(),
      // m-1：型号能力（requires_target_address）与向导数据一起取一次。
      // 失败**不**把向导整体判为加载失败（型号列表仍可用），只让能力查询
      // 落回 undefined ⇒ 走"字段缺失"的兼容分支（默认地址 1 + 提示）。
      fetchDriverTreeOnce(),
    ])
    wizardDataLoaded = true
  } catch (error) {
    wizardDataLoaded = false
    feedback.handleError(error, '创建向导数据加载失败，请重试')
  }
}

// EDGE-WIZ-003: 切换节点时也保证通道列表可见。fetchChannels 内部有
// requestSequence 防竞态: 并发请求只由最新序号写回, 不会互相覆盖。
const handleNodeChange = () => {
  selectedChannel.value = null
  // 设备上报的总线资源是「硬件ID」下拉的唯一真源：切换节点必须按新节点重新取。
  void loadNodeCapabilities(deviceForm.node_id)
  if (wizardDataLoaded && !channelsRefreshing) {
    void channelStore.fetchChannels(undefined, true).catch(() => {
      // 失败时保留已有列表, 用户仍可重试 (切回再切或重开向导)
    })
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

/**
 * 向导用的节点能力缓存（node_id → buses）。
 *
 * 「硬件ID」下拉必须来自**设备真实上报**的总线资源（见下方 availableBusesForType），
 * 因此打开向导/切换节点时按需拉取一次 GET /nodes/:id/capabilities。
 * 拉取失败时**不伪造资源**：下拉退回「该节点已有通道的 hardware_id」，仍无则显示未上报提示。
 */
const nodeCapabilitiesByNode = ref<Record<string, Record<string, any>>>({})
const capabilitiesLoading = ref(false)
const capabilitiesRequestSequence = ref(0)
const loadNodeCapabilities = async (nodeId: string | number | null | undefined) => {
  const key = String(nodeId ?? '')
  if (!key || nodeCapabilitiesByNode.value[key] || capabilitiesLoading.value) return
  const sequence = ++capabilitiesRequestSequence.value
  capabilitiesLoading.value = true
  try {
    const caps = await nodeApi.getCapabilities(key)
    if (sequence !== capabilitiesRequestSequence.value) return
    const buses = (caps as any)?.buses
    nodeCapabilitiesByNode.value = {
      ...nodeCapabilitiesByNode.value,
      [key]: buses && typeof buses === 'object' ? buses : {},
    }
  } catch (error) {
    // 能力上报读不到不是致命错误：下拉会退化为该节点已有通道，绝不伪造资源名。
    logger.warn('获取节点能力失败，硬件资源下拉回退为已有通道', { error: String(error) })
  } finally {
    if (sequence === capabilitiesRequestSequence.value) capabilitiesLoading.value = false
  }
}

/**
 * 该节点在**能力上报**里声明的总线资源 id（唯一真源）。
 *
 * 为什么必须读能力上报而不是硬编码：通道的 hardware_id 是**总线资源标识**
 * （如 UART0 / SPI2），设备实际只有它上报的那几条。改前这里在
 * 「该节点还没有此类通道」时返回硬编码的 ['UART1','UART2'] / ['I2C0','I2C1'] /
 * ['SPI1','SPI2']，既不读 capabilities、又是 1 基编号 —— 实测本机节点上报的是
 * uart=[UART0,UART1]、i2c=[I2C0]、spi=[SPI2]，于是下拉里出现设备上根本不存在的
 * UART2 / I2C1 / SPI1；用户一旦选中就会创建一个指向不存在总线的通道（静默的错误数据）。
 */
const busResourcesForType = (hardwareType: string): string[] => {
  const nodeKey = String(deviceForm.node_id ?? '')
  const buses = nodeCapabilitiesByNode.value[nodeKey]
  if (!buses) return []
  const list = (buses as Record<string, unknown>)[hardwareType.toLowerCase()]
  if (!Array.isArray(list)) return []
  return list
    .map((resource: any) => String(resource?.id ?? '').trim())
    .filter(Boolean)
}

/** 该节点是否**上报过**这个总线类型（用于区分「未上报」与「上报了 0 条」）。 */
const busTypeReported = (hardwareType: string): boolean => {
  const nodeKey = String(deviceForm.node_id ?? '')
  const buses = nodeCapabilitiesByNode.value[nodeKey]
  if (!buses) return false
  return Array.isArray((buses as Record<string, unknown>)[hardwareType.toLowerCase()])
}

/** 「硬件ID」下拉是否有可选项（无 ⇒ 必须提示未上报并禁用「下一步」）。 */
const hasBusOptions = computed(() => availableBusesForType(newChannel.hardware_type).length > 0)
/** 未上报提示文案（下拉空态与步骤校验共用，措辞只有一处）。 */
const unavailableBusMessage = computed(() =>
  `该节点未上报 ${getHardwareLabel(newChannel.hardware_type || 'BUS')} 资源，无法在此创建该总线的通道`)

/**
 * 向导「硬件ID」下拉的选项：通道 hardware_id 是**总线资源标识**语义，取值优先级：
 *   1) 该节点能力上报的资源 id（与设备一致，0 基）
 *   2) 该节点已有通道的 hardware_id（兼容能力上报缺失的老后端，但至少是真实存在的）
 * 两者都拿不到 ⇒ 空数组，由 UI 明确提示「未上报」并禁用下一步。
 * **绝不**回退到硬编码资源名 —— 那是把不存在的资源伪装成可选。
 */
const availableBusesForType = (hardwareType: string): string[] => {
  if (!deviceForm.node_id) return []
  const fromCapabilities = busResourcesForType(hardwareType)
  if (fromCapabilities.length > 0) return fromCapabilities
  // P0-2: node_id / hardware_type 比较归一化——channel.node_id 可能是
  // string(设备序列号如 'F0F5BDFFFE02')也可能是 number;channel.hardware_type
  // 后端存大写 'UART',表单是小写 'uart'。两者都需宽松匹配,否则 filter 空导致
  // 硬件ID下拉无选项。
  const nodeIdStr = String(deviceForm.node_id)
  const hwTypeLower = hardwareType.toLowerCase()
  const collectorChannels = channelStore.channels.filter(
    ch => ch && String(ch.node_id) === nodeIdStr && (ch.hardware_type || '').toLowerCase() === hwTypeLower
  )
  // 提取已有的 hardware_id（如 I2C0, UART0），去重
  return [...new Set(collectorChannels.map(ch => ch.hardware_id))].filter(Boolean) as string[]
}

// 解析器可选列表
const availableParsers = computed(() => {
  return parserStore.parsers.filter(p => p.hardware_types?.length > 0)
})

// 根据已选解析器，筛选可用的总线类型
const selectedParserBusTypes = computed(() => {
  return selectedParser.value?.hardware_types || ['i2c', 'spi', 'uart']
})

// 已有通道列表（按节点和解析器总线类型过滤）
const existingChannels = computed(() => {
  if (!selectedParser.value) return []
  const nodeIdStr = String(deviceForm.node_id ?? '')
  const parserBuses = selectedParser.value!.hardware_types.map(b => b.toLowerCase())
  return channelStore.channels.filter(ch =>
    ch && String(ch.node_id) === nodeIdStr &&
    parserBuses.includes((ch.hardware_type || '').toLowerCase())
  )
})

// 自动生成的通道名称（预览用）
const generatedChannelName = computed(() => {
  if (newChannel.hardware_type === 'i2c' && newChannel.address) {
    return `${newChannel.hardware_id}_${newChannel.address}`
  }
  if (newChannel.hardware_type === 'spi') {
    return `${newChannel.hardware_id}_CS${newChannel.spi_cs}`
  }
  return newChannel.hardware_id
})

// 是否可以进入下一步
const canGoNext = computed(() => {
  // 三步向导（2026-10-23 逻辑设备移除后原「历史数据继承」第 0 步删除）：
  //   0 选择设备型号 → 1 选择节点&通道 → 2 基本信息
  if (createStep.value === 0) return !!selectedParser.value
  if (createStep.value === 1) {
    if (!deviceForm.node_id) return false
    if (channelTab.value === 'existing') return !!selectedChannel.value
    // D6(b)：该节点未上报所选总线资源时，不允许用硬编码资源名继续创建
    // （否则会造出指向不存在总线的通道）。hasBusOptions 为假时下拉本身已禁用，
    // 这里再把「下一步」一并挡住，避免用户以为能跳过。
    if (channelTab.value === 'create') return !!newChannel.hardware_id && hasBusOptions.value
  }
  if (createStep.value === 2) return !!deviceForm.name
  return false
})

/**
 * D6(c) + m-1（2026-09-22）：把「通道的总线资源标识」解析成可提交的
 * **EdgeDevice.hardware_id（设备地址）**。
 *
 * 语义边界（本次 UI 缺陷族的根因族）：
 *   · channel.hardware_id       = 总线资源标识（UART0 / SPI2 / I2C0）
 *   · edgeDevice.hardware_id    = 该设备在总线上的**地址**（Modbus 从站号 / I2C 地址，1-254）
 * 判定口径不在这里自造：复用 @/utils/deviceAddress（与后端 deviceaction.ParseHardwareAddress
 * 同一张表，由 DeviceAddressContractParity.spec.ts 双向钉死）。
 *
 * m-1：**是否要求地址由型号能力决定，而不是由字符串形态决定**。后端
 * validateEdgeDeviceAddress 只对 driverRequiresTargetAddress 为真的型号校验 1-254，
 * 纯 I2C/SPI 型号原样接受任何标识；改前这里一律按形态兜底成默认地址 1，于是给
 * 一个从不使用地址的型号凭空写了一个地址。三分支：
 *   · requires === true      → 合法地址原样提交；总线名 ⇒ 默认地址 1 **并显式提示**；
 *   · requires === false     → **不造地址**：提交空串（后端对空值的语义是「无地址/默认」），
 *                              也不弹无意义提示；
 *   · requires === undefined → 兼容分支（老后端不返回该字段、树未加载完、或型号只在
 *                              DB 模板里）：保持**改前行为**（默认地址 1 + 提示），
 *                              宁可与后端多校验一次，也不静默放行总线名。
 */
function resolveInlineDeviceAddress(
  busHardwareId: string | undefined,
  requiresTargetAddress?: boolean,
): { value: string; warned: boolean } {
  const raw = String(busHardwareId ?? '').trim()
  const valid = isValidDeviceAddress(raw)
  const explicitValue = valid && parseDeviceAddress(raw).explicit ? raw : ''
  // 明确不消费地址的型号：不要凭空造一个它不用的地址（这是本任务的核心修复）。
  if (requiresTargetAddress === false) {
    return { value: '', warned: false }
  }
  if (valid) {
    return { value: explicitValue, warned: false }
  }
  // requires === true（真源要求地址）或 === undefined（能力未知，兼容旧后端）：
  // 退回后端默认地址 1，并由调用方显式提示。
  return { value: String(DEVICE_ADDRESS_DEFAULT), warned: true }
}

const onNewChannelBusTypeChange = () => {
  // 切换总线类型后，上一条资源 id 对新类型没有意义（UART0 不是 SPI 资源）。
  newChannel.hardware_id = ''
}

const selectParser = (parser: Parser) => {
  selectedParser.value = parser
  selectedChannel.value = null
  // EDGE-WIZ-004/005: 切换设备型号时清理旧型号的逐指令轮询间隔。
  // CreateWizardCommandIntervals 监听 deviceType 自动重置已加载指令;
  // 此处同步清空提交时使用的快照, 保证重选型号不带旧 intervals。
  commandIntervalsError.value = ''
  prepareCommandIntervals()
  // P0-1: 同步新通道硬件类型到解析器第一个可用总线,避免默认值 i2c 与
  // 解析器 hardware_types 不匹配(如选 uart 解析器但表单默认 i2c)。
  const buses = parser.hardware_types || []
  if (buses.length > 0 && !buses.includes(newChannel.hardware_type)) {
    newChannel.hardware_type = buses[0]
    newChannel.hardware_id = ''
  }
}

// ---- EDGE-WIZ-004/005: 创建向导逐指令轮询间隔 ----
// 由 CreateWizardCommandIntervals 子组件 (仅创建模式) 加载驱动 schedulable
// 指令并维护每条的间隔; 提交时经 freezeCommandIntervals 冻结到快照参与 payload。
// commandIntervalsError: 驱动指令列表加载失败时的错误信息 (用于拦截静默提交)。
const commandIntervalsRef = ref()
const commandIntervalsReady = ref(false)
const commandIntervalsSnapshot = ref<Record<string, number> | null>(null)
const commandIntervalsError = ref('')

// 当前型号驱动是否声明了 schedulable 轮询指令 — 有则隐藏全局 interval_ms
// (避免误导), 无则保留原有单个采集间隔。
const hasSchedulableCommands = computed(() => {
  const el = commandIntervalsRef.value as any
  if (!el || typeof el.schedulableCommands === 'undefined') return false
  return (el?.schedulableCommands || []).length > 0
})

const prepareCommandIntervals = () => {
  commandIntervalsReady.value = false
  commandIntervalsSnapshot.value = null
}

const handleDriverCommandsLoadError = (message: string) => {
  commandIntervalsError.value = message
  commandIntervalsReady.value = false
  commandIntervalsSnapshot.value = null
  feedback.error(message)
}

// 提交时一致快照。加载失败/进行中 → null (不携带任何 intervals, 更不会带
// 旧驱动的数据); 就绪 → 全部 schedulable 指令的当前间隔。
const freezeCommandIntervals = (): Record<string, number> | null => {
  const el = commandIntervalsRef.value as any
  if (!el || typeof el.getIntervals !== 'function') return commandIntervalsSnapshot.value
  const intervals = el.getIntervals()
  if (intervals === null) return commandIntervalsSnapshot.value
  commandIntervalsSnapshot.value = intervals
  commandIntervalsReady.value = true
  return intervals
}

const selectChannel = (ch: Channel) => {
  selectedChannel.value = ch
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

/**
 * 编辑模式的通道可选项：**当前节点**下、与设备型号总线类型相容的通道。
 *
 * 为什么按总线类型过滤：后端在保存时校验「通道的 hardware_type 与设备型号
 * 需要的总线相容」，把不相容的通道列出来等于给用户提供一个必然 400 的选项。
 * 型号未知（老数据 / 树未加载）时不过滤 —— 宁可多一个选项，也不要因为能力
 * 字段缺失就把唯一可用的通道藏起来。
 */
const editChannels = computed<Channel[]>(() => {
  const nodeIdStr = String(deviceForm.node_id ?? '')
  if (!nodeIdStr) return []
  const parserBuses = (selectedParser.value?.hardware_types || []).map(b => b.toLowerCase())
  const deviceBus = String(deviceForm.hardware_type || '').toLowerCase()
  const accepted = parserBuses.length > 0 ? parserBuses : (deviceBus ? [deviceBus] : [])
  return channelStore.channels.filter((ch) => {
    if (!ch || String(ch.node_id) !== nodeIdStr) return false
    if (accepted.length === 0) return true
    return accepted.includes(String(ch.hardware_type || '').toLowerCase())
  })
})
const editChannelsLoading = computed(() => channelStore.loading)

/**
 * 编辑模式切换「所属节点」：清空已选通道并拉取该节点的通道列表。
 *
 * 清空是**语义要求**而不是体验优化：通道 id 与节点绑定，旧通道一定不属于新节点，
 * 留着它提交必然被后端 400 拒绝（"channel does not belong to node"）。让用户
 * 重选一次，比让他看到一条看不懂的服务端错误要好。
 */
const handleEditNodeChange = () => {
  deviceForm.channel_id = null
  const nodeId = deviceForm.node_id
  if (!nodeId) return
  // 通道 store 里可能只有上一个节点的通道；按新节点拉一次（后端按 node_id 过滤）。
  void channelStore.fetchChannels(nodeId).catch(() => {
    ElMessage.warning('该节点通道列表加载失败，请稍后重试或重新打开编辑框')
  })
}

const handleEdit = (device: any) => {
  // 加载设备详情并打开编辑对话框
  editingDeviceId.value = device.id
  // 填充表单数据
  deviceForm.name = device.name
  deviceForm.node_id = device.node_id
  // 通道绑定：打开时回填当前值；用户不动它 → 提交同一个 channel_id（后端校验通过）。
  deviceForm.channel_id = typeof device.channel_id === 'number' && device.channel_id > 0
    ? device.channel_id
    : null
  // 编辑模式没有「解析器选择」步骤（selectedParser 恒为 null）⇒ editChannels
  // 拿不到解析器总线列表，会退回用设备自身的 hardware_type 过滤通道。
  selectedParser.value = null
  // 拉该节点的通道（store 里可能只有别的节点的通道）
  if (device.node_id) {
    void channelStore.fetchChannels(device.node_id).catch(() => {
      // 失败时 editChannels 可能为空，下方会显示"该节点下没有可用通道"提示
    })
  }
  deviceForm.device_type = device.device_type
  deviceForm.protocol = device.protocol || 'modbus'
  deviceForm.hardware_type = device.hardware_type || 'uart'
  // 只接受**真实存储的设备地址**（normalize 已不再用通道总线名兜底）。
  // 注意这里不能用 device.channel_hardware_id 回填 —— 那是总线名，回填即回到 G4 的老路。
  deviceForm.hardware_id = device.hardware_id || ''
  deviceForm.interval_ms = device.config?.interval_ms || 1000
  // G4：快照打开时的地址值（保存时用它判断"用户是否真的改过地址"）。
  editingDeviceAddressSnapshot.value = deviceForm.hardware_id
  showCreateDialog.value = true
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

// 表单操作
const handleCreate = async () => {
  if (!deviceFormRef.value) return
  const sessionGeneration = getSessionGeneration()
  const transactionGeneration = createTransactionGeneration
  
  try {
    await deviceFormRef.value.validate()
    assertSessionGeneration(sessionGeneration)
    if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
    if (!deviceForm.node_id) {
      ElMessage.warning('请选择所属节点')
      return
    }
    submitting.value = true
    const frozenDeviceForm = { ...deviceForm }
    const frozenNewChannel = { ...newChannel }
    const frozenParser = selectedParser.value
      ? { ...selectedParser.value, hardware_types: [...(selectedParser.value.hardware_types || [])] }
      : null
    const matchingDeviceConfig = frozenParser
      ? availableTemplates.value.find(
          template => template.parser_id === frozenParser.id || template.device_type === frozenParser.id,
        )
      : undefined
    const deviceConfigId = frozenParser?.device_config_id ?? matchingDeviceConfig?.id
    const selectedChannelConfig = selectedChannel.value?.config
    const frozenSelectedChannel = selectedChannel.value
      ? {
          ...selectedChannel.value,
          config: typeof selectedChannelConfig === 'string'
            ? (() => { try { return JSON.parse(selectedChannelConfig) } catch { return {} } })()
            : { ...(selectedChannelConfig || {}) },
        }
      : null
    const frozenChannelTab = channelTab.value
    const frozenEditingDeviceId = editingDeviceId.value
    // D6(b) 提交侧兜底：步骤校验之外，创建路径再挡一次「未上报资源」。
    if (!frozenEditingDeviceId && frozenChannelTab === 'create' && !hasBusOptions.value) {
      ElMessage.warning(unavailableBusMessage.value)
      submitting.value = false
      return
    }

    // EDGE-WIZ-004/005: 提交流程先等驱动指令加载结算 (成功或失败), 再冻结
    // 逐指令轮询间隔快照。加载失败时以下拦截, 不允许携带旧驱动的 intervals
    // 静默提交。
    await (commandIntervalsRef.value as any)?.whenLoaded?.()
    const frozenCommandIntervals = freezeCommandIntervals()

    // 编辑模式 — 字段对齐后端 UpdateDTO: name/enabled/interval_ms/hardware_id/node_id/channel_id
    // 不传 type: 设备型号绑定在编辑中不可变,且后端 G1 在 device_config_id>0 时拒绝 type。
    if (frozenEditingDeviceId) {
      // G4 编辑-保存回路：编辑对话框里**没有**设备地址输入项（见步骤 3 的表单字段：
      // 只有名称 / 采集间隔）。改前这里无条件把 deviceForm.hardware_id 发出去，而该值
      // 来自 handleEdit 的 device.hardware_id —— normalize 的旧兜底会把它填成通道总线名
      // （"UART1"），于是"打开编辑框→点保存"就把总线名写回后端（设备地址字段）。
      // 现在有两道防线：
      //   ① normalize 不再跨语义兜底（api/edgeDevice.ts），所以这里是真实存储值；
      //   ② 这里只在"用户真的改了地址"时才带上该字段；值为空/与打开时相同则
      //      **完全不发**这个 key，后端就不会有任何 hardware_id 写入。
      const openedAddress = String(editingDeviceAddressSnapshot.value ?? '')
      const currentAddress = String(frozenDeviceForm.hardware_id ?? '')
      const addressChanged = currentAddress !== openedAddress
      // 契约 §4.1（R0 主路径）：node_id 与 channel_id **一起**提交。
      // 同一 edge_devices.id ⇒ unified_data.device_id 不变 ⇒ 换节点后历史连续。
      // 后端校验「新 channel 属于新 node」，只带 node_id 会 400，故前端在
      // canGoNext / 表单规则层已强制两者同时存在。
      if (!frozenDeviceForm.channel_id) {
        ElMessage.warning('请选择通道')
        submitting.value = false
        return
      }
      await edgeDeviceApi.update(frozenEditingDeviceId, {
        name: frozenDeviceForm.name,
        node_id: String(frozenDeviceForm.node_id!),
        channel_id: frozenDeviceForm.channel_id,
        interval_ms: frozenDeviceForm.interval_ms,
        // 只有地址真的变了才带上这个 key（见上方 ②）。展开空对象 = 该 key 完全不出现在
        // 请求体里，后端 UpdateDTO 的 *string 保持 nil ⇒ 不产生任何 hardware_id 写入。
        ...(addressChanged ? { hardware_id: currentAddress } : {}),
      })
      assertSessionGeneration(sessionGeneration)
      if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      edgeDeviceStore.invalidateLists()
      edgeDeviceStore.invalidateDetail(frozenEditingDeviceId)
      ElMessage.success('设备更新成功')
      showCreateDialog.value = false
      resetCreateDialog()
      await fetchDevices(true)
      return
    }

    let channelId: number | undefined
    
    // 根据 parser 的 hardware_types 推断协议类型
    if (!frozenParser) throw new Error('请先选择设备型号')

    // EDGE-WIZ-004/005: 驱动指令加载失败必须显式处理 — 提示并让用户退回
    // 重选型号或点击重试, 绝不能静默提交 (旧驱动 intervals 已在 selectParser
    // 切换时清空, 此处兜底拦截)。
    if (commandIntervalsError.value) {
      ElMessage.warning('驱动轮询指令未成功加载，请重试或返回上一步重新选择设备型号')
      createStep.value = 3
      submitting.value = false
      return
    }
    
    // F: inline channel creation — no two-phase commit
    let targetChannel: Channel
    if (frozenChannelTab === 'create') {
      const address = frozenNewChannel.hardware_type === 'spi'
        ? frozenNewChannel.spi_cs.toString()
        : frozenNewChannel.address

      const channelPayload = {
        hardware_type: toChannelHardwareType(frozenNewChannel.hardware_type),
        hardware_id: frozenNewChannel.hardware_id,
        address: address || undefined,
        config: {
          interval_ms: frozenNewChannel.interval_ms,
          device_type: frozenParser.id,  // 传递 device_type 以便模板构建
        },
      }

      // F: 传 channel 子对象给后端,事务内创建通道+设备,消除孤儿通道风险
      const baseParams = deviceConfigId
        ? { device_config_id: deviceConfigId }
        : { type: frozenParser.id }

      // D6(c)：inline 创建路径里，EdgeDevice.hardware_id 是**设备地址**（Modbus 从站号 /
      // I2C 地址），而 channelPayload.hardware_id 是**总线资源标识**（UART0/SPI2）。
      // 二者语义不同，改前直接把总线标识当设备地址提交（前一个生产故障的根因族：
      // 派发时 ParseHardwareAddress 拒绝 "UART1"，操作静默卡 QUEUED 直到 deadline 过期）。
      // 这里：合法地址（含空/0 的默认语义）原样提交；非法（总线名形态）则退回后端默认地址 1
      // 并在确认卡片显式提示 —— 必须由用户到边缘设备详情页确认/修正实际从站地址。
      // m-1：能力来自**所选型号**（frozenParser 是提交时快照），不允许用 selectedParser
      // 之外的第二份状态，否则提交期间换型号会让地址判定与型号不一致。
      const inlineDeviceAddress = resolveInlineDeviceAddress(
        channelPayload.hardware_id,
        resolveModelRequiresTargetAddress(frozenParser.id),
      )
      if (inlineDeviceAddress.warned) {
        ElMessage.warning(`通道标识「${channelPayload.hardware_id}」是总线名称，不是设备地址；已按默认从站地址 ${DEVICE_ADDRESS_DEFAULT} 创建设备，请到设备详情确认或修正实际地址`)
      }
      await edgeDeviceApi.create({
        name: String(frozenDeviceForm.name),
        node_id: String(frozenDeviceForm.node_id!),
        hardware_id: inlineDeviceAddress.value,
        ...baseParams,
        // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
        ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
        channel: {
          hardware_type: channelPayload.hardware_type,
          hardware_id: channelPayload.hardware_id,
          address: address || undefined,
          config: channelPayload.config,
        },
      })
      assertSessionGeneration(sessionGeneration)
      if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      // Refresh channel store to pick up the newly-created channel
      await channelStore.fetchChannels(frozenDeviceForm.node_id ?? undefined)
      // No downstream targetChannel/channelId are needed on the inline path —
      // the channel was created server-side inside the device-create tx and
      // is now visible via fetchChannels. Keep targetChannel as the form data
      // (no id) only to satisfy the existing-channel branch's type contract.
      targetChannel = {
        node_id: String(frozenDeviceForm.node_id ?? ''),
        hardware_type: toChannelHardwareType(frozenNewChannel.hardware_type),
        hardware_id: frozenNewChannel.hardware_id,
        address: frozenNewChannel.address || undefined,
        interval_ms: frozenNewChannel.interval_ms,
        config: {},
      }
    } else if (frozenSelectedChannel) {
      channelId = frozenSelectedChannel.id
      targetChannel = frozenSelectedChannel

      // 已有通道场景：如果 channel.config 没有 device_type，补上
      if (!channelConfigObject(targetChannel).device_type && targetChannel.id) {
        await channelStore.updateChannel(targetChannel.id, {
          config: JSON.stringify({
            ...channelConfigObject(targetChannel),
            device_type: frozenParser.id,
          }),
        })
        assertSessionGeneration(sessionGeneration)
        if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      }

      // G3：已有通道分支与 inline-create 分支必须走**同一个**地址解析口径。
      // 改前这里直接复制 targetChannel.hardware_id（通道的总线资源标识 "UART1"），
      // 于是列表页向导的第二条创建路径绕过了 resolveInlineDeviceAddress，
      // 把总线名写进 EdgeDevice.hardware_id（设备从站地址字段）—— 与 2026-09-20
      // 生产故障同一根因，只是另一条端点/分支。
      // 现在：合法地址原样提交；总线名形态退回后端默认地址 1 并在提交前显式告知用户。
      // m-1：与 inline-create 分支同一个能力口径（同一型号 → 同一判定），也不允许
      // 把能力查询写在两个分支各自处，否则两条路径会再次漂移。
      const existingChannelDeviceAddress = resolveInlineDeviceAddress(
        targetChannel.hardware_id,
        resolveModelRequiresTargetAddress(frozenParser.id),
      )
      if (existingChannelDeviceAddress.warned) {
        ElMessage.warning(`通道标识「${targetChannel.hardware_id}」是总线名称，不是设备地址；已按默认从站地址 ${DEVICE_ADDRESS_DEFAULT} 创建设备，请到设备详情确认或修正实际地址`)
      }

      // 创建边缘设备: 根据 device_config_id 是否存在决定传 device_config_id 还是 type
      if (deviceConfigId) {
        await edgeDeviceApi.create({
          name: String(frozenDeviceForm.name),
          node_id: String(frozenDeviceForm.node_id!),
          channel_id: channelId!,
          hardware_id: existingChannelDeviceAddress.value,
          device_config_id: deviceConfigId,
            // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
          ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
        })
      } else {
        await edgeDeviceApi.create({
          name: String(frozenDeviceForm.name),
          node_id: String(frozenDeviceForm.node_id!),
          channel_id: channelId!,
          type: frozenParser.id,
          // G3：与上面 device_config_id 分支同一个解析结果（同一请求内只解析一次）
          hardware_id: existingChannelDeviceAddress.value,
            // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
          ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
        })
      }
    } else {
      throw new Error('请先选择或创建通道')
    }
    assertSessionGeneration(sessionGeneration)
    if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
    edgeDeviceStore.invalidateLists()
    
    ElMessage.success('创建成功')
    showCreateDialog.value = false
    resetCreateDialog()
    await fetchDevices(true)
  } catch (error: any) {
    if (transactionGeneration === createTransactionGeneration) feedback.handleError(error, '创建失败')
  } finally {
    if (transactionGeneration === createTransactionGeneration) submitting.value = false
  }
}

const resetCreateDialog = () => {
  createStep.value = 0
  selectedParser.value = null
  selectedChannel.value = null
  channelTab.value = 'existing'
  // EDGE-WIZ-004/005: 清空逐指令间隔快照与加载错误
  commandIntervalsError.value = ''
  commandIntervalsSnapshot.value = null
  commandIntervalsReady.value = false
  deviceForm.name = ''
  deviceForm.node_id = null
  deviceForm.interval_ms = 1000
  newChannel.hardware_type = 'i2c'
  newChannel.hardware_id = ''
  newChannel.address = ''
  newChannel.spi_cs = 10
  newChannel.interval_ms = 1000
}

/** 归一化硬件类型到 Channel 契约（UI 表单是宽松 string，此处按白名单收窄，非法值回退 i2c）。 */
const toChannelHardwareType = (value: string): Channel['hardware_type'] => {
  const lower = value.toLowerCase()
  return lower === 'uart' || lower === 'i2c' || lower === 'spi' || lower === 'adc'
    ? (lower as Channel['hardware_type'])
    : 'i2c'
}

/** 读通道 config 的对象形态（后端 Channel.Config 是 text 列，可能返回 JSON 字符串）。 */
const channelConfigObject = (ch: Channel): { device_type?: string; [key: string]: unknown } => {
  if (typeof ch.config === 'string') {
    try {
      return JSON.parse(ch.config) ?? {}
    } catch {
      return {}
    }
  }
  return ch.config ?? {}
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

// 监听对话框打开，重置向导状态
const handleCreateDialogClose = (done: () => void) => {
  if (submitting.value) return
  done()
}

// 设备上报的总线资源在进入"选择/创建通道"步骤时被消费，进入该步即确保已取到
// （用户可能已在别处选过节点，或从后续步骤返回修改，handleNodeChange 不会再触发）。
watch(createStep, (step) => {
  // 步骤编号 2026-10-23 整体前移一位（原「历史数据继承」第 0 步删除）：
  // 节点能力（buses 资源）在真正的「选择节点 & 通道」步（=1）加载。
  if (step === 1) void loadNodeCapabilities(deviceForm.node_id)
})

watch(showCreateDialog, (val) => {
  if (!val) {
    createTransactionGeneration++
    submitting.value = false
    createStep.value = 0
    selectedParser.value = null
    selectedChannel.value = null
    editingDeviceId.value = null
    // EDGE-WIZ-004/005: 关闭清空逐指令间隔快照与错误
    commandIntervalsError.value = ''
    commandIntervalsSnapshot.value = null
    commandIntervalsReady.value = false
    // 重置表单
    deviceForm.name = ''
    deviceForm.node_id = null
    deviceForm.device_type = ''
    deviceForm.protocol = 'modbus'
    deviceForm.hardware_type = 'uart'
    deviceForm.hardware_id = ''
    deviceForm.interval_ms = 1000
  } else {
    void loadCreateWizardData()
  }
})

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

onUnmounted(() => {
  createTransactionGeneration++
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

/* D6(b)：节点未上报该总线资源时的显式提示（不是一个点不动的空控件）。 */
.bus-unavailable-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 18px;
  color: var(--el-color-warning);
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

/* 对话框 */
.create-device-dialog :deep(.el-dialog__body) {
  padding-top: 10px;
}

/* P0-3: 步骤指示器标题不折行——"选择节点 & 通道"含空格和 & 在窄 dialog
   下会 wrap 成两行,破坏对齐。强制单行。 */
.create-device-dialog :deep(.el-step__title) {
  white-space: nowrap;
}

/* 向导相关样式 */
.step-tip {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  background: var(--el-color-success-light-9);
  border-radius: 6px;
  color: var(--el-color-success);
  font-size: 14px;
  margin-bottom: 20px;
}

.channel-selected-badge {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--el-color-success);
  color: var(--text-on-fill);
  display: flex;
  align-items: center;
  justify-content: center;
}

.selected-parser-badge {
  padding: 8px 12px;
  background: var(--el-color-success-light-9);
  border-radius: 6px;
  font-size: 13px;
  color: var(--el-text-color-primary);
}

.channel-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 280px;
  overflow-y: auto;
}

.channel-select-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
  position: relative;
}

.channel-select-card:hover {
  border-color: var(--el-color-primary);
}

.channel-select-card:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
}

.channel-select-card.selected {
  border-color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}

/* EDGE-WIZ-002: 通道名使用 Element Plus 正文字体 (不再用原生 <code> 等宽),
   避免与页面其他表单文本风格突兀; 硬件类型 tag + 右侧总线 ID 保留必要信息。 */
.channel-select-card .channel-name {
  font-family: var(--el-font-family);
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}

.channel-select-card.selected .channel-name {
  color: var(--el-color-success);
}

.channel-bus-id {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-left: auto;
  flex-shrink: 0;
}

.parser-id {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-left: 6px;
}

.confirm-card {
  margin-top: 20px;
  padding: 16px;
  background: var(--el-fill-color-light);
  border-radius: 8px;
}

.confirm-card h4 {
  margin: 0 0 12px;
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.confirm-items {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.confirm-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
}

.confirm-item .label {
  color: var(--el-text-color-secondary);
}

.confirm-item .value {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--el-text-color-primary);
}

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