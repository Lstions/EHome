<template>
  <div class="overview-tab">
    <!--
      只读说明：本页是**已接入字段目录**，由设备解析结果自动生成。
      这里一句话讲清它与「来源组」的分工，避免用户在两页之间困惑。
    -->
    <el-alert type="info" :closable="false" show-icon class="ov-notice" data-test="ov-readonly-notice">
      <template #title>
        本页是<strong>只读</strong>的已接入字段目录，由设备解析结果自动生成。
        需要把多台设备组合成一个数据来源（主备/复合），请到
        <el-button link type="primary" data-test="ov-goto-groups" @click="$emit('open-group')">
          「来源组」
        </el-button>
        配置。
      </template>
    </el-alert>

    <el-alert v-if="error" type="error" :closable="false" show-icon class="ov-error" data-test="ov-error">
      <template #title>
        <span>加载字段目录失败：{{ error }}</span>
        <el-button link type="primary" size="small" data-test="ov-retry" @click="$emit('retry')">重试</el-button>
      </template>
    </el-alert>

    <!--
      移动端横滚容器：本表 7 列，窄屏必然溢出。
      本仓门禁（MobileTableWrapper.spec.ts）要求每张 el-table 必须在
      .mobile-table-wrapper 祖先链上。
    -->
    <div class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        v-loading="loading"
        :data="items"
        class="ov-table"
        data-test="ov-table"
        empty-text="暂无字段 —— 设备解析成功上报后会自动出现"
      >
        <!--
          列顺序按「读数据」的思维顺序：我在看哪个设备的哪个量 →
          它是什么/叫什么 → 现在值多少 → 什么时候更新的。
        -->
        <el-table-column label="逻辑设备" min-width="140">
          <template #default="{ row }">
            <span>{{ asItem(row).logical_device_name || '—' }}</span>
            <span class="mono muted">#{{ asItem(row).logical_device_id }}</span>
          </template>
        </el-table-column>

        <!-- 数据类别：物理名 + 中文描述（描述是**补充**，不是替代物理名） -->
        <el-table-column label="数据类别" min-width="220">
          <template #default="{ row }">
            <div class="ov-category">
              <span class="mono">{{ asItem(row).category }}</span>
              <span v-if="asItem(row).label_zh" class="ov-label" data-test="ov-label">
                {{ asItem(row).label_zh }}
              </span>
            </div>
          </template>
        </el-table-column>

        <!-- 数据类型 -->
        <el-table-column label="数据类型" width="100">
          <template #default="{ row }">
            <el-tag
              v-if="asItem(row).value_type"
              size="small"
              effect="plain"
              :type="typeTagType(asValueType(row))"
              data-test="ov-value-type"
            >
              {{ typeLabel(asValueType(row)) }}
            </el-tag>
            <span v-else class="muted" data-test="ov-value-type-empty">未标注</span>
          </template>
        </el-table-column>

        <!-- 单位：单独成列（不要和值粘在一起，否则出现 "3.315V0s" 这种粘连） -->
        <el-table-column label="单位" width="80">
          <template #default="{ row }">
            <span class="mono muted">{{ asItem(row).unit || '—' }}</span>
          </template>
        </el-table-column>

        <!-- 实时值：值与单位之间留空白，不再拼接年龄 -->
        <el-table-column label="实时值" min-width="130" align="right">
          <template #default="{ row }">
            <span class="ov-value mono" data-test="ov-value">{{ fmtValue(row) }}</span>
          </template>
        </el-table-column>

        <!-- 更新时间：绝对时间 + 相对年龄，两个都要（一个能对比，一个能感知新鲜） -->
        <el-table-column label="更新时间" min-width="170">
          <template #default="{ row }">
            <span class="mono muted" data-test="ov-updated">
              {{ fmtUpdated(asItem(row)) }}
            </span>
          </template>
        </el-table-column>

        <!-- 状态：按**数据新鲜度**派生，不是配置状态 -->
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusTagType(asItem(row).status)" size="small" data-test="ov-status">
              {{ statusLabel(asItem(row).status) }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 已接入字段目录（只读）。
 *
 * # 这个组件为什么长这样（2026-10-23 重做）
 *
 * 早期版本渲染了四个**主备配置概念**的列，它们都不属于字段目录：
 *   · 「来源类型」与「来源组」描述的是"多设备里用哪台供数"的配置状态，
 *     而本页只读、用户无法在这里配置它 —— 属于「来源组」页的职责；
 *   · 「当前来源」与第一列「逻辑设备」完全重复（同一行两次显示 bms）；
 *   · 「成员」在单设备时恒为 1，零信息量。
 *
 * 现在只保留字段事实：逻辑设备 / 数据类别（+中文描述）/ 数据类型 / 单位 /
 * 实时值 / 更新时间 / 状态（= 数据新鲜度）。
 *
 * # 只读的三层保证
 *
 * 1. **组件契约**：props 只有数据，emits 只有 open-group/retry —— 类型层无写能力。
 * 2. **前端测试**：源码扫描断言无写 API + DOM 断言无写按钮。
 * 3. **后端路由**：写方法显式 405。
 */
import type { SourceOverviewItem, SourceValueType } from '@/api/sourceOverview'

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

/**
 * 把 value_type 收窄为非空类型。
 *
 * 后端可能返回空串。模板里已用 v-if 挡住，但 TypeScript 看不见 v-if 的收窄。
 */
const asValueType = (row: unknown) => asItem(row).value_type as SourceValueType

// ── 数据类型 ──
//
// 词表与后端 sensor_field_meta.value_type 一致；这里只做中文展示。
function typeLabel(t: SourceValueType): string {
  switch (t) {
    case 'number':
      return '数值'
    case 'boolean':
      return '开关'
    case 'enum':
      return '枚举'
    case 'bitmask':
      return '位掩码'
    case 'string':
      return '文本'
    default:
      return '原始'
  }
}

function typeTagType(t: SourceValueType): 'success' | 'warning' | 'info' {
  switch (t) {
    case 'number':
      return 'success'
    case 'boolean':
      return 'warning'
    default:
      return 'info'
  }
}

// ── 实时值 ──

/**
 * 格式化实时值。
 *
 * 三个必须区分的状态：
 *   null  ⇒ 「—」（从未收到数据）
 *   0     ⇒ 「0」（**合法读数**，不能显示成 —）
 *   小数  ⇒ 至多 3 位（避免 3.3530000000001 这类浮点噪声）
 *
 * 注意：**不拼接单位**。单位有独立列 —— 早期把值和单位/年龄拼成
 * "3.315V0s" 是显示缺陷（值、单位、年龄粘在一起无法阅读）。
 */
function fmtValue(row: unknown): string {
  const it = asItem(row)
  if (it.latest_value === null || it.latest_value === undefined) return '—'
  const v = it.latest_value
  if (Number.isInteger(v)) return String(v)
  return v.toFixed(3).replace(/0+$/, '').replace(/\.$/, '')
}

// ── 更新时间 ──

/**
 * 更新时间显示：绝对时间 + 相对年龄。
 *
 * 为什么两个都要：绝对时间便于对比/记录，相对年龄让人一眼判断新鲜度
 * （"5s 前"比 "2026-10-23 09:12:33" 直观得多）。
 */
function fmtUpdated(it: SourceOverviewItem): string {
  if (!it.updated_at) return '—'
  const d = new Date(it.updated_at)
  if (Number.isNaN(d.getTime())) return '—'
  const abs = d.toLocaleString('zh-CN', { hour12: false })
  const age = it.age_sec
  if (age === null || age === undefined) return abs
  return `${abs}（${fmtAge(age)}前）`
}

function fmtAge(age: number): string {
  if (age < 60) return `${age}s`
  if (age < 3600) return `${Math.floor(age / 60)}m`
  if (age < 86400) return `${Math.floor(age / 3600)}h`
  return `${Math.floor(age / 86400)}d`
}

// ── 状态（数据新鲜度）──

function statusLabel(s: string): string {
  switch (s) {
    case 'ok':
      return '正常'
    case 'delay':
      return '延迟'
    case 'stale':
      return '陈旧'
    default:
      return '无数据'
  }
}

function statusTagType(s: string): 'success' | 'warning' | 'danger' | 'info' {
  switch (s) {
    case 'ok':
      return 'success'
    case 'delay':
      return 'warning'
    case 'stale':
      return 'danger'
    default:
      return 'info'
  }
}
</script>

<style scoped>
.overview-tab {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.ov-category {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.ov-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.ov-value {
  font-variant-numeric: tabular-nums;
}
.mono {
  font-family: var(--el-font-family-mono, monospace);
}
.muted {
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}
</style>
