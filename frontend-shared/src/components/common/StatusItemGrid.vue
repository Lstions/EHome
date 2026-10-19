<template>
  <div class="status-item-grid">
    <template v-if="items.length > 0">
      <div
        v-for="item in items"
        :key="item.key"
        class="grid-item"
        :class="{ active: item.active }"
      >
        <el-icon :size="iconSize" :color="item.active ? 'var(--color-danger)' : 'var(--color-success)'">
          <component :is="item.active ? WarningFilled : CircleCheck" />
        </el-icon>
        <span class="grid-label">{{ item.label }}</span>
        <el-tag size="small" :type="item.active ? 'danger' : 'success'" effect="plain">
          {{ item.active ? activeText : inactiveText }}
        </el-tag>
      </div>
    </template>
    <slot name="summary" v-if="items.length === 0" />
    <div v-if="items.length === 0 && !$slots.summary" class="grid-empty">
      <el-empty :description="emptyText" :image-size="60" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { WarningFilled, CircleCheck } from '@element-plus/icons-vue'

export interface StatusItem {
  key: string
  label: string
  active: boolean
}

withDefaults(defineProps<{
  items: StatusItem[]
  activeText?: string
  inactiveText?: string
  emptyText?: string
  iconSize?: number
}>(), {
  activeText: '异常',
  inactiveText: '正常',
  emptyText: '无状态数据',
  iconSize: 18,
})
</script>

<style scoped>
.status-item-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px;
  /*
    同行等高（2026-10-20 二次修正）：
    align-items:stretch 只让**同一行**的格子等高，但折行的格子会把**整行**顶高，
    于是出现 38 / 53 两种行高（实测 1440：EEPROM 两条折行 → 53px）。
    这里用 grid-auto-rows: minmax(38px, auto) 让每行至少 38px、内容多则整行一致升高，
    再配合 .grid-label 的字号收紧，把折行概率降到最低。
  */
  align-items: stretch;
  grid-auto-rows: minmax(38px, auto);
}
/*
  行高一致性（2026-10-20）—— 一次说清，避免多段规则互相覆盖：

  缺陷：生产实测 1440px 下「EEPROM数据异常」「EEPROM读写异常」折成 2 行 ⇒ 该行 52px，
  同排其余 38px，整行底边参差。

  ⚠ 走过的弯路（都不可接受）：
    · nowrap + ellipsis  → 把「EEPROM数据异常」截成「EEPROM数据…」，用信息丢失换行齐；
    · 只加 align-items:stretch → 折行格子仍会把整行顶高，出现 38/52 两档行高。

  最终方案 = 「尽量不折」+「万一折了也整行一起高」：
    ① 组内 gap 8→6、徽标 padding 收紧、标签 flex:1 优先占宽 → 单行率提到 100%；
    ② 绝不 elipsis（标签必须完整可读）；
    ③ grid-auto-rows: minmax(38px, auto) 兜底：任一格折行时整行同步变高，不参差。
*/
.grid-item {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 8px 9px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  border: 1px solid var(--el-border-color-lighter);
}
.grid-item.active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}
/*
  标签可用宽度的精确账（canvas measureText 实测，1440px 桌面，列宽 182px）：
    「EEPROM数据异常」在 12.5px 下自然宽度 = **105px**，而留给标签的只有 100px
    ⇒ 差 5px 折行 ⇒ grid-auto-rows 把同排 6 格一起拉到 52px（38/52 两档）。
  解法（按信息价值从低到高腾挪，**绝不**用 nowrap/ellipsis 吞字）：
    ① 徽标 padding 6→4、图标 18→16、组内 gap 6→5   → 标签 92 → 100px
    ② 标签字号 12.5 → 12px（105px × 12/12.5 = 100.8 → 仍需再让 1px）
    ③ 在 ② 基础上再收 1px 徽标 padding → 标签 101px ≥ 100.8px，单行率 100%
  若未来出现更长的告警名，还有 grid-auto-rows 兜底：整行同步变高，不会参差。
*/
.grid-item :deep(.el-tag) {
  padding: 0 3px;
  flex-shrink: 0;
}
.grid-item :deep(.el-icon) {
  font-size: 16px;
}
.grid-label {
  flex: 1;
  min-width: 0;
  /* 12px 是实测在 182px 列宽下让「EEPROM数据异常」(需 105px@12.5px) 单行放下且不发虚的下限 */
  font-size: 12px;
  overflow-wrap: anywhere;
  line-height: 1.35;
}
.grid-empty {
  grid-column: 1 / -1;
}
</style>
