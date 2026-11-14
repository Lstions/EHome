<!--
  创建 / 编辑边缘设备对话框（原 EdgeDeviceList.vue 内联的 &lt;el-dialog&gt; 模板 + scoped 样式，纯搬移）。

  边界依据：
    · 模板与样式是「创建 / 编辑设备」这一个交互的全部 UI：页面其余部分（统计卡 / 工具栏 /
      列表 / 批量条）不引用这里任何一条 class，搬走后页面 scoped 样式里不再留孤儿选择器；
      反之这些规则的 DOM 全在这里，父组件的 scoped 属性选择器也打不进子组件内部 DOM。
    · 这里**不持有状态**：唯一入参 ctl 来自 composables/useEdgeDeviceCreate.ts
      （表单状态 / 地址能力判定 / 提交事务），其字段是页面 setup 里的同一批 ref，不是副本，
      所以页面与对话框读写的是同一份状态。
    · 三个模板 ref（deviceFormRef / channelFormRef / commandIntervalsRef）改用 function-ref：
      模板搬进子组件后，字符串 ref 只会写进子组件的 setupState，写不回 ctl 里的变量。
-->
<template>
  <!-- 创建边缘设备对话框 -->
  <el-dialog
    v-model="showCreateDialog"
    :title="editingDeviceId ? '编辑边缘设备' : '创建边缘设备'"
    :width="isMobile ? '100%' : '900px'"
    :fullscreen="isMobile"
    :top="isMobile ? '0' : '6vh'"
    :close-on-click-modal="false"
    :close-on-press-escape="!submitting"
    :show-close="!submitting"
    :before-close="handleCreateDialogClose"
    class="create-device-dialog"
  >
    <div class="edge-wizard" :class="{ 'is-mobile': isMobile }">
      <!-- PC：左侧步骤导航（只允许回看已完成步骤；前进仍走「下一步」的 canGoNext 门禁） -->
      <nav v-if="!isMobile && !editingDeviceId" class="edge-wizard__rail" aria-label="创建设备步骤">
        <button
          v-for="step in EDGE_WIZARD_STEPS"
          :key="step.id"
          type="button"
          class="edge-wizard__step"
          :class="{ 'is-active': createStep === step.id, 'is-done': createStep > step.id }"
          :disabled="step.id > createStep"
          :aria-current="createStep === step.id ? 'step' : undefined"
          :data-test="`edge-wizard-step-${step.id}`"
          @click="createStep = step.id"
        >
          <span class="edge-wizard__num">{{ createStep > step.id ? '✓' : step.id + 1 }}</span>
          <span class="edge-wizard__meta">
            <b>{{ step.title }}</b>
            <small>{{ step.desc }}</small>
          </span>
        </button>
      </nav>

      <div class="edge-wizard__main">
        <div v-if="isMobile && !editingDeviceId" class="edge-wizard__mobile-head" data-test="edge-wizard-mobile-head">
          <div class="edge-wizard__mobile-title">
            <span>步骤 {{ createStep + 1 }}/{{ EDGE_WIZARD_STEPS.length }}</span>
            <b>{{ EDGE_WIZARD_STEPS[createStep].title }}</b>
          </div>
          <el-progress :percentage="((createStep + 1) / EDGE_WIZARD_STEPS.length) * 100" :show-text="false" :stroke-width="6" />
        </div>
        <div class="edge-wizard__scroll">

    <!-- Step 0: 选择设备型号(仅创建模式)
         （2026-10-23 逻辑设备移除：原「历史数据继承」步骤整体删除 ——
          承接历史数据改由设备页的「恢复已删除设备」「合并历史数据」两个
          显式入口完成，创建流程不再做身份选择。） -->
    <div v-show="!editingDeviceId && createStep === 0" class="edge-wizard__panel" data-test="edge-wizard-panel-model">
      <header class="edge-wizard__panel-head">
        <h3>选择设备型号</h3>
        <p>设备型号决定原始字节到物理量的解析方式；可先套用配置模板自动带出型号与推荐解析器。</p>
      </header>

      <!-- 型号选择步（模板快捷选择 + 解析器卡片）在 components/EdgeDeviceCreateModelStep.vue：
           它是向导里唯一不碰表单状态的一步；步骤外壳（v-show + 面板头）留在本对话框，
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
    <div v-show="!editingDeviceId && createStep === 1" class="edge-wizard__panel" data-test="edge-wizard-panel-channel">
      <header class="edge-wizard__panel-head">
        <h3>节点与通道</h3>
        <p>选择数据从哪个节点、哪条通道进入；也可以为这台设备新建通道。</p>
      </header>
      <el-form :model="deviceForm" :rules="deviceRules">
      
      <!-- 已选解析器显示 -->
      <div class="selected-parser-badge" v-if="selectedParser">
        <span>解析器: <strong>{{ selectedParser.name }}</strong></span>
        <el-tag size="small" style="margin-left: 8px;">{{ selectedParser.id }}</el-tag>
      </div>
      
      <!-- 节点选择（Step 2） -->
      <el-form-item label="所属节点" prop="node_id" style="margin-bottom: 16px;">
        <el-select v-model="deviceForm.node_id" placeholder="选择节点" style="width: 100%;" data-test="create-node" @change="handleNodeChange">
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
          <el-form :ref="setChannelFormRef" :model="newChannel" label-position="top" style="margin-top: 12px;">
            <el-row :gutter="16">
              <el-col :xs="24" :sm="12">
                <el-form-item label="硬件类型" prop="hardware_type">
                  <el-select v-model="newChannel.hardware_type" style="width: 100%;" @change="onNewChannelBusTypeChange">
                    <el-option v-for="bus in selectedParserBusTypes" :key="bus" :label="getHardwareLabel(bus)" :value="bus" />
                  </el-select>
                </el-form-item>
              </el-col>
              <el-col :xs="24" :sm="12">
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
    <div v-show="editingDeviceId || createStep === 2" class="edge-wizard__panel" data-test="edge-wizard-panel-basic">
      <header class="edge-wizard__panel-head">
        <h3>{{ editingDeviceId ? '设备信息' : '基本信息与确认' }}</h3>
        <p>{{ editingDeviceId ? '修改设备基本信息与绑定位置；换节点/通道后历史数据仍然连续。' : '填写设备名称，确认解析器、通道与轮询间隔后创建。' }}</p>
      </header>

      <el-form :ref="setDeviceFormRef" :model="deviceForm" :rules="deviceRules" label-position="top">
        <el-form-item label="边缘设备名称" prop="name">
          <el-input v-model="deviceForm.name" placeholder="请输入边缘设备名称" data-test="edge-device-name" />
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
        :ref="setCommandIntervalsRef"
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

        </div>
      </div>
    </div>

    <template #footer>
      <template v-if="!editingDeviceId">
        <el-button
          v-if="createStep > 0"
          :style="isMobile ? { minHeight: '44px' } : undefined"
          @click="createStep--"
        >上一步</el-button>
        <el-button
          v-if="createStep < 2"
          type="primary"
          :disabled="!canGoNext"
          :style="isMobile ? { minHeight: '44px' } : undefined"
          @click="createStep++"
        >
          下一步
        </el-button>
        <el-button
          v-if="createStep === 2"
          type="primary"
          :loading="submitting"
          :style="isMobile ? { minHeight: '44px' } : undefined"
          @click="handleCreate"
        >
          创建边缘设备
        </el-button>
      </template>
      <el-button v-else type="primary" :loading="submitting" @click="handleCreate">
        保存修改
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import type { PropType } from 'vue'
import { Check } from '@element-plus/icons-vue'
import { getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'
import { useResponsive } from '@/composables/useResponsive'
import CreateWizardCommandIntervals from '@/components/device/CreateWizardCommandIntervals.vue'
import type { EdgeDeviceCreateDialogCtl } from '../composables/useEdgeDeviceCreate'
import EdgeDeviceCreateModelStep from './EdgeDeviceCreateModelStep.vue'

const props = defineProps({
  /** 创建 / 编辑向导的控制器：页面 setup 的同一批 ref + 提交事务，见 useEdgeDeviceCreate.ts。 */
  ctl: { type: Object as PropType<EdgeDeviceCreateDialogCtl>, required: true },
})

/** 与规则创建向导同断点（<768px）：全屏 + 顶部步骤条 + 单列。 */
const { isMobile } = useResponsive()

// 步骤元数据：PC 左侧栏与移动端顶部条共用（id 与 createStep 同域：0/1/2）。
const EDGE_WIZARD_STEPS = [
  { id: 0, title: '设备型号', desc: '配置模板或解析器' },
  { id: 1, title: '节点与通道', desc: '数据入口与总线参数' },
  { id: 2, title: '基本信息', desc: '名称、轮询间隔与确认' },
] as const

// 解构一次：ctl 是页面 setup 里创建的稳定对象，字段都是同一批 ref；
// 模板里由编译器逐处 unref / 写回 .value，写法与搬移前完全一致。
const {
  showCreateDialog, editingDeviceId, submitting, handleCreateDialogClose, createStep,
  selectedTemplateId, availableTemplates, selectedTemplate, availableParsers, selectedParser,
  onTemplateSelected, selectParser,
  deviceForm, deviceRules, onlineCollectors, handleNodeChange,
  channelTab, existingChannels, selectedChannel, channelCardLabel, selectChannel,
  newChannel, setChannelFormRef, onNewChannelBusTypeChange, selectedParserBusTypes,
  hasBusOptions, availableBusesForType, busTypeReported, unavailableBusMessage, capabilitiesLoading,
  collectors, editChannels, editChannelsLoading, handleEditNodeChange,
  hasSchedulableCommands, commandIntervalsSnapshot, generatedChannelName, setCommandIntervalsRef,
  handleDriverCommandsLoadError, canGoNext, handleCreate, setDeviceFormRef,
} = props.ctl
</script>

<style scoped>
/* D6(b)：节点未上报该总线资源时的显式提示（不是一个点不动的空控件）。 */
.bus-unavailable-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 18px;
  color: var(--el-color-warning);
}

/* 对话框 */
.create-device-dialog :deep(.el-dialog__body) {
  padding-top: 10px;
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

/* ============================================================================
   创建设备向导（2026-11-14 与规则向导统一）
   —— 对话框 teleport 到 body，用 :global 挂壳样式；PC 左侧栏 / 移动端全屏+顶部步骤条。
   ========================================================================== */
:global(.create-device-dialog) {
  display: flex;
  flex-direction: column;
  max-height: 88vh;
  margin-bottom: 0;
}
:global(.create-device-dialog .el-dialog__body) {
  padding: 0;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
:global(.create-device-dialog.is-fullscreen) {
  /* 解除 theme.css 的全局 .el-dialog { max-width: 92vw } 限制（否则全屏少 31px）。 */
  width: 100%;
  max-width: 100%;
  margin: 0;
  height: 100vh;
  max-height: 100vh;
}

.edge-wizard { display: flex; height: 100%; min-height: 520px; }
.edge-wizard__rail {
  width: 220px;
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 18px 12px;
  border-right: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-blank);
}
.edge-wizard__step {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  padding: 11px 12px;
  border: 0;
  border-radius: 10px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  color: var(--el-text-color-primary);
  transition: background-color .15s ease;
}
.edge-wizard__step:hover:not(:disabled) { background: var(--el-fill-color-light); }
.edge-wizard__step:disabled { cursor: not-allowed; opacity: .55; }
.edge-wizard__step.is-active { background: var(--el-color-primary-light-9); }
.edge-wizard__step.is-active .edge-wizard__num {
  background: var(--el-color-primary);
  border-color: var(--el-color-primary);
  color: var(--text-on-fill);
}
.edge-wizard__step.is-done .edge-wizard__num {
  border-color: var(--el-color-success);
  color: var(--el-color-success);
}
.edge-wizard__num {
  width: 26px;
  height: 26px;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--el-border-color);
  border-radius: 50%;
  font-size: 12px;
  font-weight: 600;
  line-height: 1;
}
.edge-wizard__meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.edge-wizard__meta b { font-size: 14px; }
.edge-wizard__meta small { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; }
.edge-wizard__main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.edge-wizard__scroll { flex: 1; overflow-y: auto; padding: 20px 24px; max-height: 62vh; }
.edge-wizard__panel-head { margin-bottom: 14px; }
.edge-wizard__panel-head h3 { margin: 0 0 4px; font-size: 16px; }
.edge-wizard__panel-head p { margin: 0; font-size: 13px; line-height: 1.5; color: var(--el-text-color-secondary); }
.edge-wizard__mobile-head { padding: 14px 16px 2px; }
.edge-wizard__mobile-title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.edge-wizard__mobile-title b { font-size: 15px; color: var(--el-text-color-primary); }

@media (max-width: 768px) {
  .edge-wizard { min-height: 100%; }
  .edge-wizard__scroll { padding: 8px 16px 24px; max-height: none; }
  .edge-wizard__panel-head { margin-bottom: 10px; }
  .channel-list { max-height: none; }
}
</style>
