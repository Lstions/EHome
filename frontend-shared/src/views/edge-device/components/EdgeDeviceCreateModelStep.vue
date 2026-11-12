<!--
  创建向导第 0 步：选设备型号（配置模板快捷选择 + 解析器卡片列表）。

  为什么切成子组件：它是整个创建向导里**唯一**不碰表单状态的一步 —— 只需要
  「模板列表 / 已选模板 / 解析器列表 / 已选解析器」四个只读值与两个选择动作。
  步骤外壳（v-show 容器与 .step-tip）留在页面：那是三步共用的向导骨架，
  搬进来会让 .step-tip 在页面与子组件各留一份，必然漂移。

  模板根是**多根片段**（el-card + 解析器列表）。这不是风格选择：页面里那段 DOM
  原本就是 step 容器 div 的两个并列子节点，多根片段才能保持 DOM 层级完全一致，
  不给父容器（flex/间距）引入任何新的盒子。
-->
<template>
    <!-- 使用配置模板（可选） -->
    <el-card shadow="never" class="template-quick-pick" v-if="templates.length > 0">
      <div class="template-pick-row">
        <div class="template-pick-label">
          <el-icon><Document /></el-icon>
          <span>使用配置模板（可选）</span>
        </div>
        <el-select
          :model-value="selectedTemplateId"
          @update:model-value="onTemplateInput"
          placeholder="选一个模板可自动套用设备类型与推荐解析器"
          clearable
          filterable
          style="flex: 1;"
          @change="onTemplateChange"
        >
          <el-option
            v-for="t in templates"
            :key="t.id"
            :label="`${t.name}  (${t.device_type} / ${getHardwareLabel(t.hardware_type)})${t.is_default ? ' ⭐' : ''}`"
            :value="t.id"
          >
            <div class="template-option-row">
              <span class="tpl-name">{{ t.name }}</span>
              <el-tag v-if="t.is_default" type="success" size="small">默认</el-tag>
              <el-tag size="small" type="info">{{ getHardwareLabel(t.hardware_type) }}</el-tag>
            </div>
          </el-option>
        </el-select>
      </div>
      <el-alert v-if="selectedTemplate" :closable="false" type="success" show-icon style="margin-top: 8px;">
        已套用模板: <strong>{{ selectedTemplate.name }}</strong>
        (设备类型 <code>{{ selectedTemplate.device_type }}</code> · 硬件 <code>{{ getHardwareLabel(selectedTemplate.hardware_type) }}</code>)
      </el-alert>
    </el-card>

    <!-- 解析器选择卡片 -->
    <div class="parser-select-list">
      <!-- I-9 A 类：解析器选择是创建设备向导第 1 步的唯一入口，改前只有 @click
           （键盘完全不可达，键盘用户无法继续向导）。补 role/tabindex/aria-label +
           Enter/Space，与同仓 .collector-card、StatCard.vue 是同一范式。
           :aria-selected 让读屏能报出当前选中的是哪一个。 -->
      <div
        v-for="parser in parsers"
        :key="parser.id"
        class="parser-select-card"
        :class="{ selected: selectedParser?.id === parser.id }"
        role="button"
        tabindex="0"
        :aria-label="`选择解析器 ${parser.name}`"
        :aria-pressed="selectedParser?.id === parser.id"
        @click="emit('select-parser', parser)"
        @keydown.enter.prevent="emit('select-parser', parser)"
        @keydown.space.prevent="emit('select-parser', parser)"
      >
        <div class="parser-select-icon">
          <el-icon :size="24"><Cpu /></el-icon>
        </div>
        <div class="parser-select-info">
          <h4>{{ parser.name }}</h4>
          <p class="parser-vendor">{{ parser.vendor }}</p>
          <div class="parser-tags">
            <el-tag v-for="bus in parser.hardware_types" :key="bus" size="small" :type="getHardwareTagType(bus)">
              {{ getHardwareLabel(bus) }}
            </el-tag>
          </div>
        </div>
        <div v-if="selectedParser?.id === parser.id" class="parser-selected-badge">
          <el-icon><Check /></el-icon>
        </div>
      </div>
    </div>
</template>

<script setup lang="ts">
import { Document, Cpu, Check } from '@element-plus/icons-vue'
import type { DeviceConfig } from '@/api/deviceConfig'
import type { Parser } from '@/api/parser'
import { getHardwareLabel, getHardwareTagType } from '@/utils/hardwareTag'

defineProps<{
  templates: DeviceConfig[]
  selectedTemplateId: number | null
  selectedTemplate: DeviceConfig | null
  parsers: Parser[]
  selectedParser: Parser | null
}>()

const emit = defineEmits<{
  (e: 'update:selectedTemplateId', id: number | null): void
  (e: 'template-change', id: number | null): void
  (e: 'select-parser', parser: Parser): void
}>()

// el-select 的 clearable 在清空时吐 undefined；统一收窄成 null，页面那个 ref 的声明类型才不会被撑宽。
const onTemplateInput = (id: number | undefined | null) => emit('update:selectedTemplateId', id ?? null)
const onTemplateChange = (id: number | undefined | null) => emit('template-change', id ?? null)
</script>

<style scoped>
/* ── 型号选择步（随模板片段从 EdgeDeviceList.vue 搬入）──
   父组件的 scoped 规则打不进子组件内部 DOM：片段搬走时样式必须跟着走，
   否则模板快捷选择卡与解析器卡片会退回无边框、无选中态的裸 div。 */
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

/* I-9 A 类：键盘焦点必须可见（否则加了 tabindex 等于给键盘用户一个隐形停靠点） */
.parser-select-card:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
}

.parser-select-card.selected {
  border-color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}

.parser-select-icon {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  background: var(--brand-gradient);
  display: flex;
  align-items: center;
  justify-content: center;
  /* 白字：底为 --brand-gradient（两端深色系）——暗色下白字最差 4.83:1，不能用 --text-on-fill。 */
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

/* 选中徽标：原先与「通道选择」的 .channel-selected-badge 是同一条合并规则，
   片段搬走后按组件边界拆开（声明一字未改，只是不再跨组件共享选择器）。 */
.parser-selected-badge {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--el-color-success);
  color: var(--text-on-fill);
  display: flex;
  align-items: center;
  justify-content: center;
}
</style>
