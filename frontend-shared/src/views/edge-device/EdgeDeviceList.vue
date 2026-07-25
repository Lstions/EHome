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
  <div class="stats-row desktop-only">
    <div class="stat-card" @click="handleStatClick('all')">
      <div class="stat-icon total">
        <el-icon><Cpu /></el-icon>
      </div>
      <div class="stat-content">
        <CountUp :value="stats.total" class="stat-value" />
        <span class="stat-label">本页边缘设备</span>
      </div>
      <div class="stat-action">
        <el-icon><Plus /></el-icon>
      </div>
    </div>
        
    <div class="stat-card online">
      <div class="stat-icon online">
        <el-icon><CircleCheck /></el-icon>
      </div>
      <div class="stat-content">
        <CountUp :value="stats.online" class="stat-value" />
        <span class="stat-label">本页在线</span>
      </div>
    </div>
        
    <div class="stat-card offline" @click="handleStatClick('offline')">
      <div class="stat-icon offline">
        <el-icon><CircleClose /></el-icon>
      </div>
      <div class="stat-content">
        <CountUp :value="stats.offline" class="stat-value" />
        <span class="stat-label">本页离线/异常</span>
      </div>
    </div>

    <div class="stat-card" @click="handleStatClick('today')">
      <div class="stat-icon today">
        <el-icon><DataAnalysis /></el-icon>
      </div>
      <div class="stat-content">
        <CountUp :value="stats.todayData" :decimals="1" suffix="k" class="stat-value" />
        <span class="stat-label">今日数据</span>
      </div>
    </div>
  </div>

  <!-- 移动端紧凑统计 -->
  <div class="mobile-stats-row mobile-only">
    <MobileStatCard
      :value="stats.total"
      label="本页边缘设备"
      :icon="Cpu"
      variant="primary"
      @click="handleStatClick('all')"
    />
    <MobileStatCard
      :value="stats.online"
      label="本页在线"
      :icon="CircleCheck"
      variant="success"
    />
    <MobileStatCard
      :value="stats.offline"
      label="本页离线/异常"
      :icon="CircleClose"
      variant="danger"
      @click="handleStatClick('offline')"
    />
    <MobileStatCard
      :value="todayDataK"
      label="今日数据"
      :icon="DataAnalysis"
      variant="warning"
      @click="handleStatClick('today')"
    />
  </div>

<!-- 工具栏 -->
    <div class="toolbar">
      <div class="toolbar-left">
        <el-input
          v-model="searchKeyword"
          placeholder="搜索边缘设备名称、类型..."
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
    <div class="batch-bar" v-if="viewMode === 'table' && selectedDevices.length > 0">
      <span class="batch-info">已选择 <strong>{{ selectedDevices.length }}</strong> 个设备</span>
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
      <div class="table-wrapper">
        <div class="table-scroll-hint">
          <el-icon><ArrowRight /></el-icon>
          <span>可横向滚动查看完整信息</span>
        </div>
        <el-table
          :data="filteredDevices"
          stripe
          @selection-change="handleSelectionChange"
          ref="tableRef"
          class="device-table"
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
      </div>
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
              <span class="fact-value" :title="device.node?.name || ('#' + device.node_id)">{{ device.node?.name || ('#' + device.node_id) }}</span>
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
          <el-button size="small" type="danger" text :icon="Delete" :aria-label="`删除 ${device.name}`" @click="handleDelete(device)" />
        </div>
      </el-card>
    </div>

    <!-- 空状态 -->
    <EmptyState
      v-if="!loading && filteredDevices.length === 0"
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
      width="680px"
      :close-on-click-modal="false"
      :close-on-press-escape="!submitting"
      :show-close="!submitting"
      :before-close="handleCreateDialogClose"
      class="create-device-dialog dialog-mobile-constrained"
    >
      <!-- 步骤指示器 -->
      <el-steps :active="createStep" finish-status="success" simple style="margin-bottom: 24px;">
        <el-step title="选择解析器" />
        <el-step title="选择节点 & 通道" />
        <el-step title="基本信息" />
      </el-steps>
      
      <!-- Step 1: 选择解析器 -->
      <div v-show="createStep === 0">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>选择此设备使用的<strong>解析器</strong>，解析器负责将原始字节转换为物理量</span>
        </div>

        <!-- 使用配置模板（可选） -->
        <el-card shadow="never" class="template-quick-pick" v-if="availableTemplates.length > 0">
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
                <el-tag v-for="bus in parser.hardware_types" :key="bus" size="small" :type="getBusTagType(bus) as any">
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
      
      <!-- Step 2: 选择/创建通道 -->
      <div v-show="createStep === 1">
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
          <el-select v-model="deviceForm.node_id" placeholder="选择节点" style="width: 100%;" @change="selectedChannel = null">
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
            <div v-if="existingChannels.length > 0" class="channel-list">
              <div
                v-for="ch in existingChannels"
                :key="ch.id"
                class="channel-select-card"
                :class="{ selected: selectedChannel?.id === ch.id }"
                @click="selectChannel(ch)"
              >
                <code>{{ ch.name || (ch.hardware_type?.toUpperCase() + ' ' + ch.hardware_id) }}</code>
                <el-tag size="small" :type="getBusTagType(ch.hardware_type) as any">{{ ch.hardware_type.toUpperCase() }}</el-tag>
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
                <el-col :xs="24" :span="12">
                  <el-form-item label="硬件类型" prop="hardware_type">
                    <el-select v-model="newChannel.hardware_type" style="width: 100%;">
                      <el-option v-for="bus in selectedParserBusTypes" :key="bus" :label="bus.toUpperCase()" :value="bus" />
                    </el-select>
                  </el-form-item>
                </el-col>
                <el-col :xs="24" :span="12">
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
      
      <!-- Step 3: 基本信息 -->
      <div v-show="createStep === 2">
        <div class="step-tip">
          <el-icon><InfoFilled /></el-icon>
          <span>填写设备<strong>基本信息</strong>并确认配置</span>
        </div>
        
        <el-form ref="deviceFormRef" :model="deviceForm" :rules="deviceRules" label-position="top">
          <el-form-item label="边缘设备名称" prop="name">
            <el-input v-model="deviceForm.name" placeholder="请输入边缘设备名称" />
          </el-form-item>
          
          <el-form-item label="采集间隔 (ms)" prop="interval_ms">
            <el-input-number v-model="deviceForm.interval_ms" :min="100" :max="3600000" :step="100" />
          </el-form-item>
        </el-form>
        
        <!-- 配置确认卡片 -->
        <div class="confirm-card">
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
            <div class="confirm-item">
              <span class="label">采集间隔</span>
              <span class="value">{{ deviceForm.interval_ms }}ms</span>
            </div>
          </div>
        </div>
      </div>
      
      <template #footer>
        <el-button v-if="createStep > 0" @click="createStep--">上一步</el-button>
        <el-button v-if="createStep < 2" type="primary" :disabled="!canGoNext" @click="createStep++">
          下一步
        </el-button>
        <el-button v-if="createStep === 2" type="primary" :loading="submitting" @click="handleCreate">
          创建边缘设备
        </el-button>
      </template>
    </el-dialog>

    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { 
  Cpu, CircleCheck, CircleClose, DataAnalysis, Grid, 
  Plus, View, Edit, Delete, InfoFilled, Check, Warning, Connection, DataLine,
  Odometer, Sunny, Cloudy, Lightning, Open, SetUp, List, Download,
  Document, ArrowRight
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
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
import MobileStatCard from '@/components/common/MobileStatCard.vue'
import { deviceTypeOptions, getDeviceTypeLabel as getGlobalDeviceTypeLabel, getDeviceTypeIcon } from '@/utils/deviceType'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'

const router = useRouter()
const route = useRoute()
const nodeStore = useNodeStore()
const channelStore = useChannelStore()
const parserStore = useParserStore()
const edgeDeviceStore = useEdgeDeviceStore()

// 视图模式：card | table
const viewMode = ref<'card' | 'table'>('card')

// 表格多选
const selectedDevices = ref<any[]>([])
// @ts-ignore - table ref used in template
const tableRef = ref()

// 状态
const collectors = ref<any[]>([])
const currentPage = ref(1)
const pageSize = ref(24)
const total = ref(0)
const searchKeyword = ref('')
const typeFilter = ref('')
const statusFilter = ref('')
const hardwareFilter = ref('')
const showCreateDialog = ref(false)
const submitting = ref(false)
let createTransactionGeneration = 0

const routeSearch = typeof route.query.search === 'string' ? route.query.search : ''
const routeStatus = typeof route.query.status === 'string' ? route.query.status : ''
if (routeSearch) searchKeyword.value = routeSearch
if (['active', 'online', 'offline', 'warning', 'error', 'disabled'].includes(routeStatus)) {
  statusFilter.value = routeStatus
}

const getListParams = () => {
  const params: any = { page: currentPage.value, page_size: pageSize.value }
  if (typeFilter.value) params.device_type = typeFilter.value
  if (statusFilter.value) params.status = statusFilter.value
  return params
}
const initialCache = edgeDeviceStore.getCachedList(getListParams())
const hasInitialCache = !!initialCache
const loading = ref(!hasInitialCache)
const devices = ref<EdgeDevice[]>(compactEdgeDeviceList(initialCache?.items))

const hasActiveFilters = computed(() => Boolean(searchKeyword.value || typeFilter.value || statusFilter.value || hardwareFilter.value))

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
  if (tpl.parser_id && availableParsers.value.length > 0) {
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

const todayDataK = computed(() => {
  const v = stats.todayData
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  return String(v)
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

// 只显示在线节点
const onlineCollectors = computed(() => collectors.value.filter((c: any) => c.status === 'online'))

// 设备类型定义 — 从 deviceType.ts 统一导入
const deviceTypes = deviceTypeOptions

// 过滤后的设备
const filteredDevices = computed(() => {
  let result = compactEdgeDeviceList(devices.value)
  
  if (searchKeyword.value) {
    const kw = searchKeyword.value.toLowerCase()
    result = result.filter(d => d.name?.toLowerCase().includes(kw))
  }
  
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

let wizardDataLoaded = false
const loadCreateWizardData = async () => {
  if (wizardDataLoaded) return
  try {
    await Promise.all([
      fetchNodes(),
      channelStore.channels.length === 0 ? channelStore.fetchChannels(undefined, true) : Promise.resolve(),
      parserStore.parsers.length === 0 ? parserStore.fetchParsers(true) : Promise.resolve(),
      loadTemplates(),
    ])
    wizardDataLoaded = true
  } catch (error) {
    wizardDataLoaded = false
    ElMessage.error('创建向导数据加载失败，请重试')
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
  const collectorChannels = channelStore.channels.filter(
    ch => ch?.node_id === deviceForm.node_id && ch.hardware_type === hardwareType
  )
  // 提取已有的 hardware_id（如 I2C0, UART1），去重
  const buses = [...new Set(collectorChannels.map(ch => ch.hardware_id))]
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

// 根据总线类型获取标签类型
const getBusTagType = (bus: string) => {
  const types: Record<string, string> = {
    uart: '', i2c: 'warning', spi: 'danger', gpio: 'success', adc: 'primary'
  }
  return types[bus] || 'info'
}

// 解析器可选列表
const availableParsers = computed(() => {
  return parserStore.parsers.filter(p => p.hardware_types.length > 0)
})

// 根据已选解析器，筛选可用的总线类型
const selectedParserBusTypes = computed(() => {
  return selectedParser.value?.hardware_types || ['i2c', 'spi', 'uart']
})

// 已有通道列表（按节点和解析器总线类型过滤）
const existingChannels = computed(() => {
  if (!selectedParser.value) return []
  return channelStore.channels.filter(ch => 
    ch?.node_id === deviceForm.node_id &&
    selectedParser.value!.hardware_types.includes(ch.hardware_type)
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
  if (createStep.value === 0) return !!selectedParser.value
  if (createStep.value === 1) {
    if (!deviceForm.node_id) return false
    if (channelTab.value === 'existing') return !!selectedChannel.value
    if (channelTab.value === 'create') return !!newChannel.hardware_id
  }
  if (createStep.value === 2) return !!deviceForm.name
  return false
})

const selectParser = (parser: Parser) => {
  selectedParser.value = parser
  selectedChannel.value = null
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

// 操作
const clearFilters = () => {
  searchKeyword.value = ''
  typeFilter.value = ''
  statusFilter.value = ''
  hardwareFilter.value = ''
  currentPage.value = 1
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

const handleDelete = async (device: any) => {
  let deleted = false
  try {
    await ElMessageBox.confirm(
      `确定要删除边缘设备 "${device.name}" 吗？`,
      '警告',
      { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }
    )

    await edgeDeviceStore.deleteDevice(device.id)
    deleted = true
    devices.value = devices.value.filter(item => item?.id !== device.id)
    total.value = Math.max(0, total.value - 1)
    updateStats()
    try {
      await fetchDevices(true, true)
    } catch {
      ElMessage.warning('设备已删除，但列表刷新失败，请稍后刷新')
    }
    ElMessage.success('删除成功')
  } catch (error: any) {
    if (error !== 'cancel' && !deleted) {
      ElMessage.error('删除失败')
    }
  }
}

// 表格多选
const handleSelectionChange = (selection: any[]) => {
  selectedDevices.value = selection
}

// 批量删除
const handleBatchDelete = async () => {
  if (selectedDevices.value.length === 0) return
  try {
    await ElMessageBox.confirm(
      `确定要删除选中的 ${selectedDevices.value.length} 个设备吗？此操作不可撤销。`,
      '批量删除确认',
      { confirmButtonText: '确认删除', cancelButtonText: '取消', type: 'warning' }
    )

    const ids = selectedDevices.value.map(d => d.id)
    const results = await Promise.allSettled(ids.map(id => edgeDeviceStore.deleteDevice(id)))
    const succeeded = results.filter(result => result.status === 'fulfilled').length
    const failed = results.length - succeeded
    const succeededIds = new Set(ids.filter((_id, index) => results[index].status === 'fulfilled'))
    devices.value = devices.value.filter(device => device && !succeededIds.has(device.id))
    total.value = Math.max(0, total.value - succeeded)
    updateStats()
    selectedDevices.value = []
    if (succeeded > 0) ElMessage.success(`成功删除 ${succeeded} 个设备`)
    if (failed > 0) ElMessage.error(`${failed} 个设备删除失败`)
    try {
      await fetchDevices(true, true)
    } catch {
      ElMessage.warning('删除结果已保存，但列表刷新失败，请稍后刷新')
    }
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error('批量删除失败')
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

    // 编辑模式 — 字段对齐后端 UpdateDTO: name/type/enabled/interval_ms/hardware_id/node_id/channel_id
    if (frozenEditingDeviceId) {
      await edgeDeviceApi.update(frozenEditingDeviceId, {
        name: frozenDeviceForm.name,
        node_id: String(frozenDeviceForm.node_id!),
        type: frozenDeviceForm.device_type,
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
    if (!frozenParser) throw new Error('请先选择解析器')
    const parserHwTypes = frozenParser.hardware_types || []
    const protocol = parserHwTypes.includes('uart') ? 'stream' : 'modbus'
    
    // 如果选了"创建新通道"，先创建通道
    let targetChannel: Channel
    if (frozenChannelTab === 'create') {
      const address = frozenNewChannel.hardware_type === 'spi'
        ? frozenNewChannel.spi_cs.toString()
        : frozenNewChannel.address
      
      const ch = await channelStore.createChannel({
        node_id: frozenDeviceForm.node_id!,
        hardware_type: frozenNewChannel.hardware_type as any,
        hardware_id: frozenNewChannel.hardware_id,
        address: address || undefined,
        status: 'active',
        config: { 
          interval_ms: frozenNewChannel.interval_ms,
          device_type: frozenParser.id  // 传递 device_type 以便模板构建
        }
      }) as Channel
      assertSessionGeneration(sessionGeneration)
      if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      channelId = ch.id
      targetChannel = ch
    } else if (frozenSelectedChannel) {
      channelId = frozenSelectedChannel.id
      targetChannel = frozenSelectedChannel
      
      // 已有通道场景：如果 channel.config 没有 device_type，补上
      if (!targetChannel.config?.device_type && targetChannel.id) {
        await channelStore.updateChannel(targetChannel.id, {
          config: JSON.stringify({
            ...targetChannel.config,
            device_type: frozenParser.id
          })
        })
        assertSessionGeneration(sessionGeneration)
        if (transactionGeneration !== createTransactionGeneration) throw new Error('创建事务已取消')
      }
    } else {
      throw new Error('请先选择或创建通道')
    }
    
    // 创建边缘设备 (后端要求通过 device_config_id 绑定设备配置，type 由后端推导)
    if (!deviceConfigId) throw new Error('所选解析器没有对应的设备配置，请刷新页面后重试')
    await edgeDeviceApi.create({
      name: String(frozenDeviceForm.name),
      node_id: String(frozenDeviceForm.node_id!),
      channel_id: channelId!,
      type: frozenParser.id,
      hardware_id: targetChannel.hardware_id,
      device_config_id: deviceConfigId,
    })
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

.desktop-only {
  display: contents;
}

.mobile-only {
  display: none;
}

.mobile-stats-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

@media (max-width: 768px) {
  .stats-row.desktop-only {
    display: none;
  }
  .mobile-stats-row.mobile-only {
    display: grid;
  }
}

@media (max-width: 360px) {
  .mobile-stats-row.mobile-only {
    grid-template-columns: 1fr;
  }
}

/* 统计 */
.stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
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
  min-width: 0;
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
  flex-shrink: 0;
}

.stat-icon.total { background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%); }
.stat-icon.online { background: var(--el-color-success); }
.stat-icon.offline { background: var(--el-text-color-secondary); }
.stat-icon.today { background: var(--el-color-warning); }

.stat-content { flex: 1; min-width: 0; }
.stat-value { display: block; font-size: 28px; font-weight: 600; color: var(--el-text-color-primary); }
.stat-label { font-size: 13px; color: var(--el-text-color-secondary); }

.stat-card .stat-action {
  opacity: 0;
  transition: opacity 0.2s;
  color: var(--el-color-primary);
  flex-shrink: 0;
}

.stat-card:hover .stat-action {
  opacity: 1;
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  .stat-card {
    padding: 12px;
    gap: 10px;
    border-radius: 10px;
  }
  .stat-icon {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    font-size: 22px;
  }
  .stat-value {
    font-size: 22px;
  }
  .stat-label {
    font-size: 12px;
  }
  .stat-action {
    display: none;
  }
  .device-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }
  .device-card :deep(.el-card__body) {
    padding: 14px;
  }
  .card-header {
    padding: 14px;
  }
  .card-facts {
    padding: 0 14px 14px;
  }
  .card-reading {
    padding: 10px 14px;
  }
  .card-actions {
    padding: 8px 14px;
  }
  .toolbar {
    padding: 12px;
    gap: 10px;
  }
  .toolbar-left, .toolbar-right {
    width: 100%;
    flex-wrap: wrap;
  }
  .search-input {
    width: 100%;
    min-width: 0;
  }
}

@media (max-width: 480px) {
  .stats-row {
    gap: 8px;
  }
  .stat-card {
    padding: 10px;
    gap: 8px;
  }
  .stat-icon {
    width: 36px;
    height: 36px;
    font-size: 20px;
  }
  .stat-value {
    font-size: 20px;
  }
  .stat-label {
    font-size: 11px;
  }
  .toolbar-left :deep(.el-select) {
    flex: 1 1 calc(50% - 5px);
    min-width: 0;
  }
  .toolbar-right {
    justify-content: space-between;
  }
  .toolbar-right :deep(.el-button) {
    flex: 1;
  }
}

@media (max-width: 360px) {
  .stats-row {
    grid-template-columns: 1fr;
  }
  .stat-card {
    padding: 12px;
  }
  .toolbar-left :deep(.el-select) {
    flex: 1 1 100%;
  }
  .card-facts {
    grid-template-columns: 1fr;
  }
  .card-reading {
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
  }
  .reading-value {
    white-space: normal;
    word-break: break-word;
    max-width: 100%;
  }
}

/* 工具栏 */
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  background: var(--el-bg-color);
  padding: 16px 20px;
  border-radius: 12px;
  border: 1px solid var(--el-border-color);
}

.toolbar-left {
  display: flex;
  flex: 1 1 auto;
  gap: 12px;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
}

.toolbar-right {
  display: flex;
  flex: 0 0 auto;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.search-input {
  width: 280px;
  min-width: 200px;
  flex: 1 1 220px;
}

/* 设备网格 */
.device-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
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

.channel-select-card code {
  font-size: 13px;
  color: var(--el-text-color-primary);
}

.channel-bus-id {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-left: auto;
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

.device-table {
  min-width: 900px;
}

.table-wrapper {
  width: 100%;
  overflow-x: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}

.table-scroll-hint {
  display: none;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

@media (max-width: 768px) {
  .table-scroll-hint {
    display: flex;
  }
  .device-table {
    min-width: 720px;
  }
}

@media (max-width: 480px) {
  .device-table {
    min-width: 640px;
  }
}

/* 响应式 */
@media (max-width: 1200px) {
  .stats-row { grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); }
}

@media (max-width: 1100px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
  }
  .toolbar-left {
    width: 100%;
  }
  .toolbar-right {
    width: 100%;
    justify-content: space-between;
  }
  .search-input {
    width: 100%;
    flex: 1 1 100%;
  }
}

@media (max-width: 768px) {
  .stats-row { grid-template-columns: 1fr; }
  .toolbar { flex-direction: column; gap: 12px; }
  .toolbar-left { width: 100%; flex-wrap: wrap; }
  .toolbar-right { width: 100%; }
  .search-input { width: 100%; }
  .device-grid { grid-template-columns: 1fr; }
}

@media (max-width: 600px) {
  .toolbar-left :deep(.el-select) {
    width: 100%;
  }
  .toolbar-right {
    flex-direction: column;
    align-items: stretch;
  }
  .toolbar-right :deep(.el-button-group),
  .toolbar-right :deep(.el-button) {
    width: 100%;
  }
}

@media (max-width: 360px) {
  .card-header {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }
  .device-info {
    width: 100%;
  }
  .status-indicator {
    align-self: flex-end;
  }
  .card-facts {
    grid-template-columns: 1fr;
  }
  .card-reading {
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
  }
  .reading-value {
    white-space: normal;
    word-break: break-word;
    max-width: 100%;
  }
  .card-actions {
    width: 100%;
    justify-content: space-between;
  }
  .device-grid { gap: 12px; }
}
</style>
