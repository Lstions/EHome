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
      <!-- 顶部统计 -->
      <div class="stats-row">
        <StatCard label="本页边缘设备" mobile-label="边缘设备" icon-color="var(--el-color-primary)" @click="handleStatClick('all')">
          <template #icon><el-icon><Cpu /></el-icon></template>
          <template #value><CountUp :value="stats.total" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页在线" mobile-label="在线" icon-color="var(--el-color-success)">
          <template #icon><el-icon><CircleCheck /></el-icon></template>
          <template #value><CountUp :value="stats.online" class="stat-value" /></template>
        </StatCard>

        <StatCard label="本页离线/异常" mobile-label="离线/异常" icon-color="var(--el-color-danger)" @click="handleStatClick('offline')">
          <template #icon><el-icon><CircleClose /></el-icon></template>
          <template #value><CountUp :value="stats.offline" class="stat-value" /></template>
        </StatCard>

        <StatCard label="今日数据" icon-color="var(--el-color-info)" @click="handleStatClick('today')">
          <template #icon><el-icon><DataAnalysis /></el-icon></template>
          <template #value><CountUp :value="todayDataDisplay.value" :decimals="todayDataDisplay.decimals" :suffix="todayDataDisplay.suffix" class="stat-value" /></template>
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
        
        <el-select v-model="statusFilter" placeholder="状态" clearable style="min-width: 90px;">
          <el-option label="在线" value="active" />
          <el-option label="离线" value="offline" />
          <el-option label="警告" value="warning" />
          <el-option label="故障" value="error" />
          <el-option label="已禁用" value="disabled" />
        </el-select>
        
        <el-select v-model="hardwareFilter" placeholder="硬件类型" clearable style="min-width: 120px;">
          <el-option label="UART" value="uart" />
          <el-option label="I2C" value="i2c" />
          <el-option label="SPI" value="spi" />
          <el-option label="GPIO" value="gpio" />
          <el-option label="ADC" value="adc" />
        </el-select>
      </div>
      
      <div class="toolbar-right">
        <el-button-group style="margin-right: 12px;">
          <el-button :type="viewMode === 'card' ? 'primary' : ''" aria-label="卡片视图" @click="viewMode = 'card'">
            <el-icon><Grid /></el-icon>
          </el-button>
          <el-button :type="viewMode === 'table' ? 'primary' : ''" aria-label="表格视图" @click="viewMode = 'table'">
            <el-icon><List /></el-icon>
          </el-button>
        </el-button-group>
        <el-button type="primary" @click="showCreateDialog = true">
          <el-icon><Plus /></el-icon>
          创建边缘设备
        </el-button>
      </div>
    </div>

    <div v-if="hasActiveFilters" class="active-filters" aria-label="当前筛选条件">
      <span class="active-filters-label">当前筛选：</span>
      <el-tag v-if="searchKeyword" closable @close="searchKeyword = ''">关键词：{{ searchKeyword }}</el-tag>
      <el-tag v-if="typeFilter" closable @close="typeFilter = ''">类型：{{ getDeviceTypeLabel(typeFilter) }}</el-tag>
      <el-tag v-if="statusFilter" closable @close="statusFilter = ''">状态：{{ statusLabel(statusFilter) }}</el-tag>
      <el-tag v-if="hardwareFilter" closable @close="hardwareFilter = ''">总线：{{ hardwareFilter.toUpperCase() }}</el-tag>
      <el-button text type="primary" @click="clearFilters">清除全部</el-button>
    </div>

    <!-- 批量操作栏 -->
    <div class="batch-bar" v-if="viewMode === 'table' && (selectedDevices?.length || 0) > 0">
      <span class="batch-info">已选择 <strong>{{ selectedDevices?.length || 0 }}</strong> 个设备</span>
      <div class="batch-actions">
        <el-button type="danger" size="small" @click="handleBatchDelete">
          <el-icon><Delete /></el-icon>
          批量删除
        </el-button>
        <el-button type="primary" size="small" @click="handleBatchExport">
          <el-icon><Download /></el-icon>
          批量导出CSV
        </el-button>
      </div>
    </div>

    <!-- 设备表格列表 -->
    <el-card v-if="viewMode === 'table'" shadow="hover">
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
            <code>{{ row.hardware_type?.toUpperCase() }} {{ row.hardware_id }}</code>
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
        <el-table-column label="操作" width="160" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="goToDetail(row.id)">
              <el-icon><View /></el-icon>
            </el-button>
            <el-button size="small" @click="handleEdit(row)">
              <el-icon><Edit /></el-icon>
            </el-button>
            <el-button size="small" type="danger" text @click="handleDelete(row)">
              <el-icon><Delete /></el-icon>
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 设备卡片列表 -->
    <div class="device-grid" v-if="viewMode === 'card'">
      <el-card 
        v-for="device in filteredDevices" 
        :key="device.id" 
        class="device-card"
        :class="{ offline: device.status === 'offline' || device.status === 'disabled' }"
        shadow="hover"
      >
        <div class="card-header">
          <div class="device-icon" :class="getDeviceClass(device.device_type)">
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

        <div class="card-facts">
          <div class="fact-item">
            <el-icon :size="16"><Connection /></el-icon>
            <div class="fact-content">
              <span class="fact-label">所属节点</span>
              <span
                class="fact-value copyable"
                :title="`点击复制：${device.node?.name || ('#' + device.node_id)}`"
                @click="copyText(device.node?.name || ('#' + device.node_id))"
              >{{ device.node?.name || ('#' + device.node_id) }}</span>
            </div>
          </div>
          <div class="fact-item">
            <el-icon :size="16"><DataLine /></el-icon>
            <div class="fact-content">
              <span class="fact-label">总线通道</span>
              <code class="fact-value">{{ device.hardware_type?.toUpperCase() }} {{ device.hardware_id }}</code>
            </div>
          </div>
        </div>

        <div class="card-reading" :class="{ 'no-data': !device.last_data }">
          <template v-if="device.last_data">
            <span class="reading-label">最新读数</span>
            <strong class="reading-value" :title="formatDeviceData(device.last_data)">{{ formatDeviceData(device.last_data) }}</strong>
            <span class="reading-time">{{ formatRelativeTime(device.last_data_time) }}</span>
          </template>
          <template v-else>
            <el-icon :size="16"><Warning /></el-icon>
            <span>等待首条采集数据</span>
          </template>
        </div>

        <div class="card-actions">
          <el-button size="small" type="primary" text :icon="View" @click="goToDetail(device.id)">详情</el-button>
          <el-button size="small" text :icon="Edit" @click="handleEdit(device)">编辑</el-button>
          <el-button size="small" type="danger" text :icon="Delete" :aria-label="`删除 ${device.name}`" @click="handleDelete(device)">删除</el-button>
        </div>
      </el-card>
    </div>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && (filteredDevices?.length || 0) === 0"
      icon="Cpu"
      title="暂无边缘设备"
      description='进入节点详情页，点击“创建边缘设备”按钮开始添加'
      :quick-actions="[
        { label: '查看节点', icon: View, type: 'primary', handler: () => router.push('/node') }
      ]"
    />

    <!-- 分页 -->
    <div v-if="total > pageSize" class="pagination-wrapper">
      <el-pagination
        v-model:current-page="currentPage"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[12, 24, 48]"
        layout="total, sizes, prev, pager, next"
        @current-change="() => fetchDevices()"
        @size-change="() => fetchDevices()"
      />
    </div>

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
        <el-step title="历史数据继承" />
        <el-step title="选择设备型号" />
        <el-step title="选择节点 & 通道" />
        <el-step title="基本信息" />
      </el-steps>

      <!-- Step 0: 历史数据继承 (方案 v3.3 §3.1/§3.2, 默认"作为新设备创建") -->
      <div v-show="!editingDeviceId && createStep === 0">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>此设备是否要<strong>继承历史数据</strong>？默认作为新设备创建；若为更换/重建的同一台物理设备，可继承其历史数据</span>
        </div>

        <el-radio-group v-model="inheritMode" class="inherit-mode-radio">
          <el-radio value="new">作为新设备创建（默认，新建独立逻辑身份）</el-radio>
          <el-radio value="inherit">继承历史数据（从候选逻辑设备中选择）</el-radio>
        </el-radio-group>

        <div v-if="inheritMode === 'inherit'" class="inherit-candidate-area">
          <LogicalDeviceCandidateSelect
            v-model="inheritLogicalDeviceId"
            :type="selectedParser?.id || ''"
            :node-id="deviceForm.node_id ? String(deviceForm.node_id) : ''"
            :hardware-id="inheritCandidateHardwareId"
            :channel-id="inheritCandidateChannelId"
          />
        </div>
      </div>

      <!-- Step 1: 选择设备型号(仅创建模式) -->
      <div v-show="!editingDeviceId && createStep === 1">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>选择此设备使用的<strong>设备型号</strong>，设备型号决定了原始字节到物理量的解析方式</span>
        </div>

        <!-- 使用配置模板（可选） -->
        <el-card shadow="never" class="template-quick-pick" v-if="availableTemplates?.length > 0">
          <div class="template-pick-row">
            <div class="template-pick-label">
              <el-icon><Document /></el-icon>
              <span>使用配置模板（可选）</span>
            </div>
            <el-select
              v-model="selectedTemplateId"
              placeholder="选一个模板可自动套用设备类型与推荐解析器"
              clearable
              filterable
              style="flex: 1;"
              @change="onTemplateSelected"
            >
              <el-option
                v-for="t in availableTemplates"
                :key="t.id"
                :label="`${t.name}  (${t.device_type} / ${t.hardware_type?.toUpperCase()})${t.is_default ? ' ⭐' : ''}`"
                :value="t.id"
              >
                <div class="template-option-row">
                  <span class="tpl-name">{{ t.name }}</span>
                  <el-tag v-if="t.is_default" type="success" size="small">默认</el-tag>
                  <el-tag size="small" type="info">{{ t.hardware_type?.toUpperCase() }}</el-tag>
                </div>
              </el-option>
            </el-select>
          </div>
          <el-alert v-if="selectedTemplate" :closable="false" type="success" show-icon style="margin-top: 8px;">
            已套用模板: <strong>{{ selectedTemplate.name }}</strong>
            (设备类型 <code>{{ selectedTemplate.device_type }}</code> · 硬件 <code>{{ selectedTemplate.hardware_type?.toUpperCase() }}</code>)
          </el-alert>
        </el-card>

        <!-- 解析器选择卡片 -->
        <div class="parser-select-list">
          <div
            v-for="parser in availableParsers"
            :key="parser.id"
            class="parser-select-card"
            :class="{ selected: selectedParser?.id === parser.id }"
            @click="selectParser(parser)"
          >
            <div class="parser-select-icon">
              <el-icon :size="24"><Cpu /></el-icon>
            </div>
            <div class="parser-select-info">
              <h4>{{ parser.name }}</h4>
              <p class="parser-vendor">{{ parser.vendor }}</p>
              <div class="parser-tags">
                <el-tag v-for="bus in parser.hardware_types" :key="bus" size="small" :type="getHardwareTagType(bus) as any">
                  {{ bus.toUpperCase() }}
                </el-tag>
              </div>
            </div>
            <div v-if="selectedParser?.id === parser.id" class="parser-selected-badge">
              <el-icon><Check /></el-icon>
            </div>
          </div>
        </div>
      </div>
      
      <!-- Step 2: 选择/创建通道(仅创建模式) -->
      <div v-show="!editingDeviceId && createStep === 2">
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
              <div
                v-for="ch in existingChannels"
                :key="ch.id"
                class="channel-select-card"
                :class="{ selected: selectedChannel?.id === ch.id }"
                @click="selectChannel(ch)"
              >
                <span class="channel-name" :title="ch.name || `${(ch.hardware_type || 'BUS').toUpperCase()} ${ch.hardware_id}`">{{ ch.name || `${(ch.hardware_type || 'BUS').toUpperCase()} ${ch.hardware_id}` }}</span>
                <el-tag size="small" :type="getHardwareTagType(ch.hardware_type) as any">{{ (ch.hardware_type || '').toUpperCase() }}</el-tag>
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
                    <el-select v-model="newChannel.hardware_type" style="width: 100%;">
                      <el-option v-for="bus in selectedParserBusTypes" :key="bus" :label="bus.toUpperCase()" :value="bus" />
                    </el-select>
                  </el-form-item>
                </el-col>
                <el-col :span="12">
                  <el-form-item label="硬件ID" prop="hardware_id">
                    <el-select v-model="newChannel.hardware_id" style="width: 100%;">
                      <el-option v-for="bus in availableBusesForType(newChannel.hardware_type)" :key="bus" :label="bus" :value="bus" />
                    </el-select>
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
      
      <!-- Step 3: 基本信息(创建模式 createStep===3;编辑模式直接显示) -->
      <div v-show="editingDeviceId || createStep === 3">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>{{ editingDeviceId ? '修改设备基本信息(设备型号与通道绑定不可在编辑中变更)' : '填写设备基本信息并确认配置' }}</span>
        </div>

        <el-form ref="deviceFormRef" :model="deviceForm" :rules="deviceRules" label-position="top">
          <el-form-item label="边缘设备名称" prop="name">
            <el-input v-model="deviceForm.name" placeholder="请输入边缘设备名称" />
          </el-form-item>

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
                <el-tag size="small">{{ selectedChannel?.hardware_type?.toUpperCase() || newChannel.hardware_type.toUpperCase() }}</el-tag>
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
          <el-button v-if="createStep < 3" type="primary" :disabled="!canGoNext" @click="createStep++">
            下一步
          </el-button>
          <el-button v-if="createStep === 3" type="primary" :loading="submitting" @click="handleCreate">
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

    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import {
  Cpu, CircleCheck, CircleClose, DataAnalysis, Grid,
  Plus, View, Edit, Delete, InfoFilled, Check, Warning, Connection, DataLine,
  List, Download, Document
} from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useNodeStore } from '@/stores/node'
import { useChannelStore } from '@/stores/channel'
import { useParserStore } from '@/stores/parser'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { compactEdgeDeviceList, edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import client from '@/api/client'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import CountUp from '@/components/common/CountUp.vue'
import StatCard from '@/components/common/StatCard.vue'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import DeviceBatchDeleteDialog from '@/components/device/DeviceBatchDeleteDialog.vue'
import LogicalDeviceCandidateSelect from '@/components/device/LogicalDeviceCandidateSelect.vue'
import CreateWizardCommandIntervals from '@/components/device/CreateWizardCommandIntervals.vue'
import { deviceTypeOptions, getDeviceTypeLabel as getGlobalDeviceTypeLabel, getDeviceTypeIcon } from '@/utils/deviceType'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { useWebSocketStore } from '@/stores/websocket'
import { getHardwareTagType } from '@/utils/hardwareTag'
import { useDebouncedSearch } from '@/composables/useDebouncedSearch'
import { useDeviceDelete } from '@/composables/useDeviceDelete'

const router = useRouter()
const route = useRoute()
const nodeStore = useNodeStore()
const channelStore = useChannelStore()
const parserStore = useParserStore()
const edgeDeviceStore = useEdgeDeviceStore()

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

const getListParams = () => {
  const params: any = { page: currentPage.value, page_size: pageSize.value }
  if (typeFilter.value) params.device_type = typeFilter.value
  if (statusFilter.value) params.status = statusFilter.value
  return params
}
const initialCache = edgeDeviceStore.getCachedList(getListParams())
const hasInitialCache = !!initialCache
const loading = ref(!hasInitialCache)
devices.value = compactEdgeDeviceList(initialCache?.items)

const hasActiveFilters = computed(() => Boolean(searchKeyword.value || typeFilter.value || statusFilter.value || hardwareFilter.value))

// 搜索 debounce — 减少不必要的 filter 计算
const {
  searchKeyword,
  debouncedKeyword: _debouncedSearchKeyword,
  filteredItems: _searchFilteredItems,
  clear: clearSearch,
} = useDebouncedSearch(devices, {
  searchFields: (d) => [d.name || '', d.device_type || '', getDeviceTypeLabel(d.device_type) || ''],
})

// 路由参数初始化（必须在 useDebouncedSearch 之后）
if (routeSearch) searchKeyword.value = routeSearch
if (['active', 'online', 'offline', 'warning', 'error', 'disabled'].includes(routeStatus)) {
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
  },
})

// 向导状态
const createStep = ref(0)
const selectedParser = ref<Parser | null>(null)
const selectedChannel = ref<Channel | null>(null)
const channelTab = ref('existing')

// 方案 v3.3 §3.1/§3.2 — 步骤 0"历史数据继承": 默认"作为新设备创建"。
// inheritMode='inherit' 时必须选中一个候选才可进入下一步 (显式语义)。
const inheritMode = ref<'new' | 'inherit'>('new')
const inheritLogicalDeviceId = ref<number | null>(null)
// 候选权重计算上下文: 创建位置的 hardware_id/channel_id (步骤 2 之后才有值;
// 步骤 0 时为空 → 服务端按同 type 档排序, 用户回到步骤 0 时自动重查)。
const inheritCandidateHardwareId = computed(() => {
  if (channelTab.value === 'existing') return selectedChannel.value?.hardware_id || ''
  return newChannel.hardware_id || ''
})
const inheritCandidateChannelId = computed(() => selectedChannel.value?.id)

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
    availableTemplates.value = res.list || []
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

// 设备表单
const deviceForm = reactive({
  node_id: null as number | null,
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
  offline: 0,
  todayData: 0
})

// 获取今日数据统计
const fetchTodayDataCount = async () => {
  try {
    const response = await client.get<unknown, any>('/api/v1/overview')
    stats.todayData = response.data_count_today || 0
  } catch {
    // fallback: 0
  }
}

// 今日数据展示：<1000 显示原始整数（避免 "0.0k"），>=1000 以千为单位带一位小数
const todayDataDisplay = computed(() => {
  const count = stats.todayData
  return count >= 1000
    ? { value: count / 1000, decimals: 1, suffix: 'k' }
    : { value: count, decimals: 0, suffix: '' }
})

// 只显示在线节点
const onlineCollectors = computed(() => collectors.value.filter((c: any) => c.status === 'online'))

// 设备类型定义 — 从 deviceType.ts 统一导入
const deviceTypes = deviceTypeOptions

// 过滤后的设备 — 结合 debounce 搜索与其他筛选
const filteredDevices = computed(() => {
  let result = _searchFilteredItems.value

  if (typeFilter.value) {
    result = result.filter(d => d.device_type === typeFilter.value)
  }

  if (statusFilter.value) {
    result = result.filter(d => d.status === statusFilter.value)
  }

  if (hardwareFilter.value) {
    result = result.filter(d => d.hardware_type === hardwareFilter.value)
  }

  return result
})

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
    if (sequence === listRequestSequence && !throwOnError) ElMessage.error('获取边缘设备列表失败')
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
const loadCreateWizardData = async () => {
  if (wizardDataLoaded) {
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
    ])
    wizardDataLoaded = true
  } catch (error) {
    wizardDataLoaded = false
    ElMessage.error('创建向导数据加载失败，请重试')
  }
}

// EDGE-WIZ-003: 切换节点时也保证通道列表可见。fetchChannels 内部有
// requestSequence 防竞态: 并发请求只由最新序号写回, 不会互相覆盖。
const handleNodeChange = () => {
  selectedChannel.value = null
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
  stats.online = allVisible.filter(d => d.status === 'active' || d.status === 'online').length
  stats.offline = allVisible.filter(d => d.status === 'offline' || d.status === 'disabled' || d.status === 'error' || d.status === 'warning').length
  fetchTodayDataCount()
}

// 获取可用总线（从该节点的已有通道中提取，fallback 到默认选项）
const availableBusesForType = (hardwareType: string): string[] => {
  if (!deviceForm.node_id) return []
  // P0-2: node_id / hardware_type 比较归一化——channel.node_id 可能是
  // string(设备序列号如 'F0F5BDFFFE02')也可能是 number;channel.hardware_type
  // 后端存大写 'UART',表单是小写 'uart'。两者都需宽松匹配,否则 filter 空导致
  // 硬件ID下拉无选项且无 fallback。
  const nodeIdStr = String(deviceForm.node_id)
  const hwTypeLower = hardwareType.toLowerCase()
  const collectorChannels = channelStore.channels.filter(
    ch => ch && String(ch.node_id) === nodeIdStr && (ch.hardware_type || '').toLowerCase() === hwTypeLower
  )
  // 提取已有的 hardware_id（如 I2C0, UART1），去重
  const buses = [...new Set(collectorChannels.map(ch => ch.hardware_id))].filter(Boolean)
  // 如果已有总线为空（该节点还没有任何此类通道），提供默认选项
  if (buses.length === 0) {
    switch (hardwareType) {
      case 'uart': return ['UART1', 'UART2']
      case 'i2c': return ['I2C0', 'I2C1']
      case 'spi': return ['SPI1', 'SPI2']
      case 'gpio': return ['GPIO0', 'GPIO2', 'GPIO4']
      case 'adc': return ['ADC1', 'ADC2']
      default: return []
    }
  }
  return buses
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
  // 步骤 0 历史数据继承: 始终可过。候选列表依赖设备型号 (步骤 1 才选),
  // 首次进入步骤 0 时 type 未知, 候选组件显示"先选型号"引导; 用户选"继承"
  // 后可先进步骤 1 选型号, 再回到步骤 0 选候选 (组件随 type 变化自动重查)。
  // 若选"继承"却未选候选, 在提交时拦截并提示 (见 handleCreate)。
  if (createStep.value === 0) return true
  if (createStep.value === 1) return !!selectedParser.value
  if (createStep.value === 2) {
    if (!deviceForm.node_id) return false
    if (channelTab.value === 'existing') return !!selectedChannel.value
    if (channelTab.value === 'create') return !!newChannel.hardware_id
  }
  if (createStep.value === 3) return !!deviceForm.name
  return false
})

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
  ElMessage.error(message)
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

// 设备类型相关
const getDeviceIcon = (type: string) => {
  return getDeviceTypeIcon(type)
}

const getDeviceClass = (type: string) => {
  const classMap: Record<string, string> = {
    temp_humidity: 'temp',
    wind_speed: 'wind',
    wind_direction: 'wind',
    rain: 'rain',
    light: 'light',
    battery: 'battery',
    inverter: 'solar'
  }
  return classMap[type] || 'default'
}

const getDeviceTypeLabel = (type: string) => {
  return getGlobalDeviceTypeLabel(type)
}

// Health status tag type mapping (Element Plus tag types)
function statusTagType(status: string): string {
  switch (status) {
    case 'active': return 'success'
    case 'online': return 'success'
    case 'warning': return 'warning'
    case 'error': return 'danger'
    case 'disabled': return 'info'
    case 'offline': return 'info'
    default: return 'info'
  }
}

// Health status label mapping
function statusLabel(status: string): string {
  switch (status) {
    case 'active': return '在线'
    case 'online': return '在线'
    case 'warning': return '警告'
    case 'error': return '故障'
    case 'disabled': return '已禁用'
    case 'offline': return '离线'
    default: return status || '未知'
  }
}

// 复制到剪贴板（跟随 FirmwareManage 的复制交互模式）
const copyText = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制: ' + text)
  } catch {
    ElMessage.error('复制失败，请手动复制: ' + text)
  }
}

const handleStatClick = (status: string) => {
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

const handleEdit = (device: any) => {
  // 加载设备详情并打开编辑对话框
  editingDeviceId.value = device.id
  // 填充表单数据
  deviceForm.name = device.name
  deviceForm.node_id = device.node_id
  deviceForm.device_type = device.device_type
  deviceForm.protocol = device.protocol || 'modbus'
  deviceForm.hardware_type = device.hardware_type || 'uart'
  deviceForm.hardware_id = device.hardware_id || ''
  deviceForm.interval_ms = device.config?.interval_ms || 1000
  showCreateDialog.value = true
}

// 删除确认弹窗 (方案 v3.3 §2.1: ElDialog + radio + 异步逻辑设备信息)
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
      `${device.hardware_type?.toUpperCase()} ${device.hardware_id}`,
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
    // 方案 v3.3 §3.2: 用户勾选了"继承历史数据"但未选中候选 (候选依赖型号,
    // 步骤 0 可先跳过) 时, 在提交处拦截——继承语义要求显式目标, 不能静默
    // 回退为"作为新设备创建"违背用户的显式选择。
    if (inheritMode.value === 'inherit' && inheritLogicalDeviceId.value === null) {
      ElMessage.warning('已选择"继承历史数据"但未选中候选逻辑设备，请回到"历史数据继承"步骤选择候选，或改为"作为新设备创建"')
      return
    }
    submitting.value = true
    const frozenDeviceForm = { ...deviceForm }
    const frozenNewChannel = { ...newChannel }
    const frozenParser = selectedParser.value
      ? { ...selectedParser.value, hardware_types: [...(selectedParser.value.hardware_types || [])] }
      : null
    // 方案 v3.3 §3.2: 步骤 0 继承选择快照 — "继承历史数据"且已选候选时
    // 携带 logical_device_id; "作为新设备创建"不传 (后端新建逻辑身份)。
    const frozenInheritParams = inheritMode.value === 'inherit' && inheritLogicalDeviceId.value !== null
      ? { logical_device_id: inheritLogicalDeviceId.value }
      : {}
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

    // EDGE-WIZ-004/005: 提交流程先等驱动指令加载结算 (成功或失败), 再冻结
    // 逐指令轮询间隔快照。加载失败时以下拦截, 不允许携带旧驱动的 intervals
    // 静默提交。
    await (commandIntervalsRef.value as any)?.whenLoaded?.()
    const frozenCommandIntervals = freezeCommandIntervals()

    // 编辑模式 — 字段对齐后端 UpdateDTO: name/enabled/interval_ms/hardware_id/node_id/channel_id
    // 不传 type: 设备型号绑定在编辑中不可变,且后端 G1 在 device_config_id>0 时拒绝 type。
    if (frozenEditingDeviceId) {
      await edgeDeviceApi.update(frozenEditingDeviceId, {
        name: frozenDeviceForm.name,
        node_id: String(frozenDeviceForm.node_id!),
        hardware_id: frozenDeviceForm.hardware_id,
        interval_ms: frozenDeviceForm.interval_ms,
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
        hardware_type: frozenNewChannel.hardware_type as any,
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

      await edgeDeviceApi.create({
        name: String(frozenDeviceForm.name),
        node_id: String(frozenDeviceForm.node_id!),
        hardware_id: channelPayload.hardware_id,
        ...baseParams,
        ...frozenInheritParams,
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
      await channelStore.fetchChannels(frozenDeviceForm.node_id)
      // No downstream targetChannel/channelId are needed on the inline path —
      // the channel was created server-side inside the device-create tx and
      // is now visible via fetchChannels. Keep targetChannel as the form data
      // (no id) only to satisfy the existing-channel branch's type contract.
      targetChannel = { ...frozenNewChannel } as Channel
    } else if (frozenSelectedChannel) {
      channelId = frozenSelectedChannel.id
      targetChannel = frozenSelectedChannel

      // 已有通道场景：如果 channel.config 没有 device_type，补上
      if (!targetChannel.config?.device_type && targetChannel.id) {
        await channelStore.updateChannel(targetChannel.id, {
          config: JSON.stringify({
            ...targetChannel.config,
            device_type: frozenParser.id,
          }),
        })
        assertSessionGeneration(sessionGeneration)
        if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      }

      // 创建边缘设备: 根据 device_config_id 是否存在决定传 device_config_id 还是 type
      if (deviceConfigId) {
        await edgeDeviceApi.create({
          name: String(frozenDeviceForm.name),
          node_id: String(frozenDeviceForm.node_id!),
          channel_id: channelId!,
          hardware_id: targetChannel.hardware_id,
          device_config_id: deviceConfigId,
          ...frozenInheritParams,
          // EDGE-WIZ-004: 逐指令轮询间隔 (仅驱动声明 schedulable 指令时携带)
          ...(frozenCommandIntervals ? { command_intervals: frozenCommandIntervals } : {}),
        })
      } else {
        await edgeDeviceApi.create({
          name: String(frozenDeviceForm.name),
          node_id: String(frozenDeviceForm.node_id!),
          channel_id: channelId!,
          type: frozenParser.id,
          hardware_id: targetChannel.hardware_id,
          ...frozenInheritParams,
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
    if (transactionGeneration === createTransactionGeneration) ElMessage.error(error.message || '创建失败')
  } finally {
    if (transactionGeneration === createTransactionGeneration) submitting.value = false
  }
}

const resetCreateDialog = () => {
  createStep.value = 0
  selectedParser.value = null
  selectedChannel.value = null
  channelTab.value = 'existing'
  inheritMode.value = 'new'
  inheritLogicalDeviceId.value = null
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

const formatRelativeTime = (time: string) => {
  if (!time) return '-'
  const now = new Date()
  const date = new Date(time)
  const diff = now.getTime() - date.getTime()
  
  const minutes = Math.floor(diff / 60000)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (minutes < 1440) return `${Math.floor(minutes / 60)}小时前`
  return `${Math.floor(minutes / 1440)}天前`
}

// 检查是否有特定类型数据
const hasSpecificData = (device: any): boolean => {
  const d = device.last_data
  return !!(d?.temperature !== undefined || d?.humidity !== undefined ||
             d?.pressure !== undefined || d?.wind_speed !== undefined)
}

// 格式化通用数据为可读字符串
const formatDeviceData = (data: any): string => {
  const entries = Object.entries(data).filter(([k]) =>
    !['timestamp', 'device_id', 'device_type', 'channel_id'].includes(k))
  if (entries.length === 0) return ''
  return entries.map(([k, v]) => {
    const num = Number(v)
    const val = Number.isFinite(num) ? num.toFixed(2) : v
    return `${k}: ${val}`
  }).join(' | ')
}

// 监听筛选条件变化，重新获取数据
watch([typeFilter, statusFilter], () => {
  currentPage.value = 1
  fetchDevices()
})

// 清空筛选时同时清空搜索
const clearFilters = () => {
  clearSearch()
  typeFilter.value = ''
  statusFilter.value = ''
  hardwareFilter.value = ''
  currentPage.value = 1
}

// 监听对话框打开，重置向导状态
const handleCreateDialogClose = (done: () => void) => {
  if (submitting.value) return
  done()
}

watch(showCreateDialog, (val) => {
  if (!val) {
    createTransactionGeneration++
    submitting.value = false
    createStep.value = 0
    selectedParser.value = null
    selectedChannel.value = null
    editingDeviceId.value = null
    inheritMode.value = 'new'
    inheritLogicalDeviceId.value = null
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
  fetchDevices()
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

.template-quick-pick {
  margin-bottom: 16px;
  border: 1px dashed var(--border-color);
  background: var(--bg-color-secondary);
}
.template-pick-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.template-pick-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: var(--el-text-color-regular);
  white-space: nowrap;
}
.template-option-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
}
.template-option-row .tpl-name {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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
  color: #fff;
  font-size: 20px;
}

.stat-icon.total { background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%); }
.stat-icon.online { background: var(--el-color-success); }
.stat-icon.offline { background: var(--el-text-color-secondary); }
.stat-icon.today { background: var(--el-color-warning); }

.stat-content { flex: 1; }
.stat-value { display: block; font-size: 28px; font-weight: 600; color: var(--el-text-color-primary); }
.stat-label { font-size: 13px; color: var(--el-text-color-secondary); }


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

.device-card {
  overflow: hidden;
  transition: transform 0.2s ease, box-shadow 0.2s ease, border-color 0.2s ease;
  border: 1px solid var(--el-border-color);
}

.device-card:hover {
  transform: translateY(-2px);
  border-color: var(--el-color-primary-light-5);
  box-shadow: var(--shadow-md);
}

.device-card.offline { opacity: 0.78; }

.device-card .el-card__body {
  padding: 0;
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

.device-icon {
  width: 40px;
  height: 40px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  flex-shrink: 0;
}

.device-icon.wind { color: var(--el-text-color-regular); background: var(--el-fill-color-light); }
.device-icon.rain { color: var(--el-color-warning); background: var(--el-color-warning-light-9); }
.device-icon.light { color: var(--el-color-warning); background: var(--el-color-warning-light-9); }
.device-icon.battery { color: var(--el-color-success); background: var(--el-color-success-light-9); }
.device-icon.solar { color: var(--el-color-primary); background: var(--el-color-primary-light-9); }

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

.card-facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  padding: 0 16px 16px;
}

.fact-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
  color: var(--el-text-color-secondary);
}

.fact-item > .el-icon {
  margin-top: 2px;
  color: var(--el-text-color-secondary);
  flex-shrink: 0;
}

.fact-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.fact-label,
.reading-label {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.fact-value {
  color: var(--el-text-color-primary);
  font-size: 13px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

code.fact-value {
  color: var(--el-color-primary);
  font-family: var(--el-font-family);
}

.fact-value.copyable {
  cursor: pointer;
}

.fact-value.copyable:hover {
  color: var(--el-color-primary);
}

.card-reading {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr) max-content;
  align-items: center;
  gap: 10px;
  min-height: 52px;
  padding: 12px 16px;
  background: var(--el-fill-color-lighter);
  border-top: 1px solid var(--el-border-color-lighter);
}

.card-reading.no-data {
  display: flex;
  justify-content: center;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.reading-value {
  min-width: 0;
  color: var(--el-text-color-primary);
  font-size: 14px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.reading-time {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  white-space: nowrap;
}

.card-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  padding: 10px 16px;
  border-top: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
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

/* 方案 v3.3 §3.1 — 步骤 0 历史数据继承 */
.inherit-mode-radio {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 16px;
}

.inherit-candidate-area {
  margin-top: 4px;
  padding: 12px;
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
}

.parser-select-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-height: 360px;
  overflow-y: auto;
}

.parser-select-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
}

.parser-select-card:hover {
  border-color: var(--el-color-primary);
}

.parser-select-card.selected {
  border-color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}

.parser-select-icon {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
}

.parser-select-info {
  flex: 1;
}

.parser-select-info h4 {
  margin: 0 0 4px;
  font-size: 14px;
  color: var(--el-text-color-primary);
}

.parser-vendor {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.parser-tags {
  display: flex;
  gap: 4px;
}

.parser-selected-badge,
.channel-selected-badge {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--el-color-success);
  color: #fff;
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
  justify-content: space-between;
  align-items: center;
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
