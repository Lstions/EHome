<template>
  <el-card class="firmware-filter-card" shadow="never">
    <el-form :inline="true" class="firmware-filter-form" @submit.prevent>
      <el-form-item label="节点">
        <el-input
          v-model="filters.node_id"
          placeholder="节点 ID（如 F0F5BD02F35C）"
          clearable
          data-testid="firmware-filter-node-id"
          style="width: 220px;"
          @keyup.enter="emit('search')"
          @clear="emit('search')"
        />
        <span class="filter-hint">填节点后按该节点档案判定兼容性</span>
      </el-form-item>
      <el-form-item label="chip">
        <el-select v-model="filters.chip" placeholder="全部" clearable style="width: 130px;" @change="emit('search')">
          <el-option label="全部" value="" />
          <el-option label="esp32c6" value="esp32c6" />
          <el-option label="esp32s3" value="esp32s3" />
        </el-select>
      </el-form-item>
      <el-form-item label="Flash">
        <el-select v-model="filters.flash_mb" placeholder="全部" clearable style="width: 110px;" @change="emit('search')">
          <el-option label="全部" value="" />
          <el-option label="8 MB" value="8" />
          <el-option label="16 MB" value="16" />
        </el-select>
      </el-form-item>
      <el-form-item label="渠道">
        <el-select v-model="filters.channel" placeholder="全部" clearable style="width: 120px;" @change="emit('search')">
          <el-option label="全部" value="" />
          <el-option label="稳定" value="stable" />
          <el-option label="灰度" value="beta" />
          <el-option label="内部" value="internal" />
        </el-select>
      </el-form-item>
      <el-form-item label="关键词">
        <el-input
          v-model="filters.q"
          placeholder="版本 / 文件名 / 档位"
          clearable
          data-testid="firmware-filter-q"
          style="width: 200px;"
          @keyup.enter="emit('search')"
          @clear="emit('search')"
        />
      </el-form-item>
      <el-form-item>
        <el-checkbox v-model="filters.include_withdrawn" data-testid="firmware-filter-withdrawn" @change="emit('search')">
          含已撤回
        </el-checkbox>
      </el-form-item>
      <el-form-item>
        <el-button type="primary" :icon="Search" data-testid="firmware-filter-search" @click="emit('search')">查询</el-button>
        <el-button :icon="RefreshLeft" data-testid="firmware-filter-reset" @click="onReset">重置</el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<script setup lang="ts">
import { RefreshLeft, Search } from '@element-plus/icons-vue'
import { createFirmwareFilters, type FirmwareFilters } from '../firmwarePage'

/**
 * 固件筛选条（M1b / T1b.1/T1b.3 拆出的子组件）。
 *
 * 字段与后端 §4.3 契约严格对齐；旧参数 `status`/`model` **不再出现**（后端不声明）。
 *
 * 这里直接改 props 对象的**属性**（不是重新赋值 props）：filters 是父级传入的
 * reactive 对象，子组件只做绑定，避免「本地副本 + 深比较回写」这层无收益的间接。
 */
const props = defineProps<{
  filters: FirmwareFilters
}>()

const emit = defineEmits<{
  (e: 'search'): void
  (e: 'reset'): void
}>()

const filters = props.filters

const onReset = () => {
  Object.assign(filters, createFirmwareFilters())
  emit('reset')
}
</script>

<style scoped>
.firmware-filter-card {
  margin-bottom: 12px;
}
.firmware-filter-form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
}
.firmware-filter-form :deep(.el-form-item) {
  margin-bottom: 8px;
  margin-right: 8px;
}
.filter-hint {
  margin-left: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
