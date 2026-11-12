<template>
  <el-card class="toolbar-card" shadow="hover">
    <div class="filter-bar">
      <div class="filter-left">
        <el-input
          v-model="keyword"
          placeholder="搜索模板名称..."
          prefix-icon="Search"
          clearable
          class="search-input"
          @input="emit('search')"
        />

        <el-select v-model="type" placeholder="设备类型" clearable class="filter-select">
          <template #prefix>
            <el-icon><Grid /></el-icon>
          </template>
          <el-option v-for="t in deviceTypeOptions" :key="t.value" :label="t.label" :value="t.value" />
        </el-select>

        <el-select v-model="hardware" placeholder="硬件类型" clearable class="filter-select">
          <el-option label="UART" value="uart" />
          <el-option label="I2C" value="i2c" />
          <el-option label="SPI" value="spi" />
          <el-option label="ADC" value="adc" />
        </el-select>

        <el-select v-model="status" placeholder="状态" clearable class="filter-select">
          <el-option label="启用" value="active" />
          <el-option label="禁用" value="inactive" />
        </el-select>
      </div>

      <slot name="actions" />
    </div>
  </el-card>
</template>

<script setup lang="ts">
import { Grid } from '@element-plus/icons-vue'
import { deviceTypeOptions } from '@/utils/deviceType'

/**
 * 工具栏卡片：搜索框 + 三个筛选下拉；右侧动作组以 #actions 插槽承接。
 *
 * 边界依据：.filter-right 的 scoped 规则被 DeviceConfigList.spec.ts 直接从
 * **页面** scoped CSS 断言（flex-wrap，360px 裁切钉子），所以动作组的模板与
 * 样式必须留在页面 —— 这里只把卡片壳与左侧筛选控件搬进来，插槽内容由页面
 * 作用域编译，页面样式照常命中。四个筛选值用 defineModel 原样保留双向绑定，
 * el-input 的 @input 仍冒泡成同一时机的 search 事件（页面接 handleSearch）。
 */
const keyword = defineModel<string>('keyword', { required: true })
const type = defineModel<string>('type', { required: true })
const hardware = defineModel<string>('hardware', { required: true })
const status = defineModel<string>('status', { required: true })
const emit = defineEmits<{ search: [] }>()
</script>

<style scoped>
.toolbar-card :deep(.el-card__body) {
  padding: 16px 20px;
}

.filter-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
}

.filter-left {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.search-input {
  width: 240px;
}

.filter-select {
  width: 140px;
}

@media (max-width: 768px) {
  .filter-bar { flex-direction: column; align-items: stretch; }
  .filter-left { flex-direction: column; align-items: stretch; }
  .filter-left { width: 100%; }
  .search-input, .filter-select { width: 100%; }
}
</style>
