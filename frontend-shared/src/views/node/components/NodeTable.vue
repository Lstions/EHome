<template>
  <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper） -->
  <div class="mobile-table-wrapper">
    <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
  <el-table 
    :data="nodes" 
    v-loading="loading"
    stripe
    @row-click="(row) => goToDetail(row.node_id)"
    row-class-name="collector-row"
  >
    <el-table-column label="节点" min-width="200">
      <template #default="{ row }">
        <div class="table-collector-info">
          <div class="collector-icon" :class="row.status">
            <el-icon><Cpu /></el-icon>
          </div>
          <div>
            <div class="name">{{ nodeDisplayName(row) }}</div>
            <div class="model">{{ row.model || '未知型号' }}</div>
          </div>
        </div>
      </template>
    </el-table-column>
    
    <el-table-column prop="node_id" label="设备ID" width="100" />
    
    <el-table-column prop="firmware_version" label="固件" width="100">
      <template #default="{ row }">
        <el-tag size="small">{{ row.firmware_version || UNKNOWN }}</el-tag>
      </template>
    </el-table-column>
    
    <el-table-column label="状态" width="100">
      <template #default="{ row }">
        <div class="status-cell" :class="row.status">
          <span class="status-dot"></span>
          {{ row.status === 'online' ? '在线' : '离线' }}
        </div>
      </template>
    </el-table-column>
    
    <el-table-column label="连接质量" width="150">
      <template #default="{ row }">
        <div class="quality-bar" v-if="row.status === 'online'">
          <el-progress 
            :percentage="row.connection_quality || 0" 
            :stroke-width="6"
            :color="getQualityColor(row.connection_quality)"
            :show-text="false"
          />
          <span class="quality-value">{{ row.connection_quality || 0 }}%</span>
        </div>
        <span v-else>-</span>
      </template>
    </el-table-column>
    
    <el-table-column label="延迟" width="100">
      <template #default="{ row }">
        <span v-if="row.latency_ms > 0" :style="{ color: getLatencyColor(row.latency_ms) }">
          {{ row.latency_ms }} ms
        </span>
        <span v-else>-</span>
      </template>
    </el-table-column>
    
    <el-table-column label="上线时间" width="160">
      <template #default="{ row }">
        <span class="time">{{ formatRelativeTime(row.last_online_time) }}</span>
      </template>
    </el-table-column>
    
    <!-- 操作列只需容纳"详情/删除"两个 2 字 text 按钮：120px（原 240px 在 390px
         视口下占 62%，@360 达 69%，把固定列变成整屏）。触控热区由 .touch-target 补足。 -->
    <el-table-column label="操作" width="120" fixed="right">
      <template #default="{ row }">
        <el-button size="small" type="primary" text class="touch-target" @click.stop="goToDetail(row.node_id)">详情</el-button>
        <el-button size="small" type="danger" text class="touch-target" :aria-label="`删除 ${row.name}`" @click.stop="handleDelete(row)">删除</el-button>
      </template>
    </el-table-column>
  </el-table>
  </div>
</template>

<script setup lang="ts">
import { Cpu } from '@element-plus/icons-vue'
import { UNKNOWN } from '@/utils/format'
import { nodeDisplayName } from '@/utils/nodeDisplay'
import { getLatencyColor, getQualityColor } from '@/utils/theme'
import { formatRelativeTime } from '../nodeListFormat'

/**
 * 节点列表视图（移动端宽表 + 横滑提示）。
 *
 * 边界依据：整段是自包含模板片段 —— 只读一批节点行、只上抛「进详情 / 删除」两个动作。
 * 分页器**不在这里**：listPageConventions 以「路由页面模板里有 el-pagination」判定列表页，
 * 分页器留在 NodeList.vue 才能继续被门禁看见（页码/页长/请求也仍由页面 composable 收口）。
 *
 * 注意：新组件注释里不得出现 el-table 的标签原文 —— MobileTableWrapper 门禁按
 * 标签字面量 grep 定分母，注释命中会被误收成「有表却 0 张表」（本文件确有表格，不受影响）。
 */
defineProps<{
  nodes: any[]
  loading: boolean
}>()

const emit = defineEmits<{
  (e: 'open', nodeId: string): void
  (e: 'delete', node: any): void
}>()

// 纯转发：沿用原模板调用名与实参形态，行为不变。
const goToDetail = (nodeId: string) => emit('open', nodeId)
const handleDelete = (node: any) => emit('delete', node)
</script>

<style scoped>
/* 列表视图：只保留表格内专属规则；.collector-icon / .quality-bar / .quality-value
   与卡片视图共用 → 末尾以 <style scoped src> 引入 nodeList.shared.css。 */
.collector-row {
  cursor: pointer;
}

.table-collector-info {
  display: flex;
  align-items: center;
  gap: 12px;
}

.table-collector-info .name {
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.table-collector-info .model {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.status-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}

.status-cell .status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.status-cell.online .status-dot { background: var(--el-color-success); }
.status-cell.offline .status-dot { background: var(--el-text-color-secondary); }
</style>

<style scoped src="./nodeList.shared.css"></style>
