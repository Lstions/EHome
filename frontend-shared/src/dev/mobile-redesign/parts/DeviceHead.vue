<!--
  设备身份页头 —— BMS 移动端改版 demo 专用（src/dev）。

  ── 它修的是什么（用户批评 3：顶部重复内容）─────────────────────────────────
  生产页把**同一组事实**说了两遍：
    ① 面包屑「边缘设备管理 / BMS 设备详情」
    ② 标题行「bms 在线 [jiabaida_bms · 16S]」
    ③ 副行「节点 F0F5BD02DD84 · 通道 UART0 · 设备 #14 · 节点固件 2.8.0 · 轮询已启用 1/3 条」
  其中「BMS」在 ①②各一次，「设备/设备号」在②③重复；
  下面再跟 4 个按钮「同步到HA / 编辑 / 删除 / 刷新」，393px 下**排成两行**，
  实测页头高 300px（≈0.34 屏）却只承载 3 条有效信息。

  ── 三个方案的页头 ────────────────────────────────────────────────────────
    · stacked ：面包屑压成返回箭头；标题 + 状态一行；元信息收进一行可横滑的 chips；
                 主操作「刷新」常驻，其余进 ⋯ —— 2 行信息 + 1 行操作
    · compact ：进一步合并：标题行右侧直接挂「在线」+ 时间戳；元信息整体收进
                 点开的底部抽屉（默认只显示 节点号 · 通道），操作也是 2 个
    · sheet   ：标题行只留 名称 + 在线 + ⋯；元信息与操作全部进「设备信息」底部抽屉，
                 首屏只剩 1 行 —— 首屏指标卡能往上提 100px

  所有档位都满足：主操作（刷新）不必进菜单；删除这种破坏性动作永远不在一级。
-->
<template>
  <header class="dev-head" :class="`v-${variant}`">
    <!-- 第一行：返回 + 名称 + 在线 + 更多 -->
    <div class="head-row">
      <button type="button" class="back-btn" aria-label="返回边缘设备列表" @click="emit('back')">
        <el-icon :size="17"><ArrowLeft /></el-icon>
      </button>

      <div class="ident">
        <h1 class="dev-name" :title="DEVICE.name">{{ DEVICE.name }}</h1>
        <span class="dev-state" :class="DEVICE.online ? 'is-on' : 'is-off'">
          <span class="state-dot" aria-hidden="true"></span>{{ DEVICE.online ? '在线' : '离线' }}
        </span>
        <!-- compact：时间戳贴在标题行右侧，省掉一整行 -->
        <span v-if="variant === 'compact'" class="head-time">{{ DEVICE.updatedAt }}</span>
      </div>

      <div class="head-actions">
        <el-button
          type="primary"
          size="small"
          :icon="Refresh"
          class="refresh-btn"
          aria-label="刷新数据"
          @click="emit('refresh')"
        >{{ variant === 'sheet' ? '' : '刷新' }}</el-button>
        <!-- ⋯ 菜单：编辑 / 同步到 HA / 删除 -->
        <el-dropdown trigger="click" popper-class="dev-head-more" @command="onCommand">
          <el-button size="small" :icon="MoreFilled" circle aria-label="更多设备操作" />
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="edit" :icon="Edit">编辑设备</el-dropdown-item>
              <el-dropdown-item command="sync" :icon="Connection">同步到 HA</el-dropdown-item>
              <el-dropdown-item command="delete" :icon="Delete" divided>
                <span class="danger-item">删除设备</span>
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </div>

    <!--
      第二行（stacked / compact）：元信息。
      初版用横向滚动条（overflow-x:auto + nowrap），实测两个问题：
        ① scrollWidth 461 > clientWidth 369 ⇒ 页面里出现唯一一处"能看到但读不到"的内容，
           用户不会想到去横滑一个没有滚动条提示的区域；
        ② 最后一个 chip 被裁掉一半，视觉上像渲染坏了。
      改为 flex-wrap 自然折行：3~4 个 chip 一行、两行共约 44px，
      信息全部可见，且比"藏起来一半"更符合用户"要用满空间"的诉求。
      节点号是 12 位大写串，单独给等宽字体，便于与节点页对照。
    -->
    <div v-if="variant !== 'sheet'" class="meta-strip" role="list" aria-label="设备元信息">
      <span class="meta-chip" role="listitem">
        <el-icon :size="11"><Cpu /></el-icon>嘉佰达 · {{ DEVICE.cellCount }}S
      </span>
      <span class="meta-chip is-mono" role="listitem">
        {{ DEVICE.nodeSn }}
      </span>
      <span class="meta-chip" role="listitem">{{ DEVICE.channel }} · {{ DEVICE.address }}</span>
      <span class="meta-chip is-quiet" role="listitem">固件 {{ DEVICE.firmware }}</span>
      <span class="meta-chip is-quiet" role="listitem">轮询 {{ pollEnabled }}/{{ pollLimit }}</span>
    </div>

    <!-- sheet：一行摘要 + 打开抽屉 -->
    <button v-else type="button" class="info-sheet-trigger" @click="emit('openInfo')">
      <el-icon :size="12"><Cpu /></el-icon>
      <span>嘉佰达 BMS · {{ DEVICE.cellCount }}S · {{ DEVICE.channel }} {{ DEVICE.address }}</span>
      <el-icon :size="12" class="trigger-arrow"><ArrowRight /></el-icon>
    </button>
  </header>
</template>

<script setup lang="ts">
import { Refresh, MoreFilled, Edit, Connection, Delete, ArrowLeft, ArrowRight, Cpu } from '@element-plus/icons-vue'
import { DEVICE } from '../mockBms'

withDefaults(defineProps<{
  /** stacked / compact / sheet */
  variant?: 'stacked' | 'compact' | 'sheet'
  pollEnabled?: number
  pollLimit?: number
}>(), {
  variant: 'stacked',
  pollEnabled: 1,
  pollLimit: 3,
})

const emit = defineEmits<{
  (e: 'back'): void
  (e: 'refresh'): void
  (e: 'openInfo'): void
  (e: 'command', name: string): void
}>()

/** ⋯ 菜单分发：删除走二次确认（demo 里只回传，不真删） */
function onCommand(cmd: string) {
  emit('command', cmd)
}
</script>

<style scoped>
.dev-head {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px 8px;
  background: var(--card-bg);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

/* ── 第一行 ── */
.head-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.back-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  /* 44px 热区（规范 §4.4.5），用负 margin 收视觉重量 */
  width: 36px;
  height: 36px;
  margin-left: -8px;
  border: none;
  border-radius: 50%;
  background: none;
  color: var(--el-text-color-regular);
  cursor: pointer;
  flex-shrink: 0;
}
.back-btn:hover { background: var(--el-fill-color-light); }

.ident {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  flex: 1 1 auto;
}
.dev-name {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
  line-height: 1.2;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.dev-state {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 11px;
  line-height: 1.5;
}
.dev-state.is-on {
  color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}
.dev-state.is-off {
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
}
.state-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}
.head-time {
  flex-shrink: 0;
  margin-left: auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
}

.head-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
/* 刷新按钮在手机上不加文字宽度，靠 primary 色与图标识别 */
.v-sheet .refresh-btn { width: 32px; padding: 0; }
.head-actions :deep(.el-button) { height: 30px; }
.head-actions :deep(.el-button.is-circle) { width: 30px; padding: 0; }

/* ── 元信息 chips（横滑，不折行 → 不贡献高度） ── */
.meta-strip {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 6px;
  min-width: 0;
}
.meta-chip {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex: 0 0 auto;
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-regular);
  font-size: 11px;
  line-height: 1.5;
  white-space: nowrap;
}
.meta-chip.is-quiet {
  background: transparent;
  border: 1px solid var(--el-border-color-lighter);
  color: var(--el-text-color-secondary);
}
/* 节点号：等宽 + 略小，避免 12 位大写串在 chip 里显得过长 */
.meta-chip.is-mono {
  font-family: 'SF Mono', Consolas, Monaco, monospace;
  font-size: 10px;
  letter-spacing: 0.2px;
}
.meta-chip :deep(.el-icon) { color: var(--el-text-color-secondary); }

/* ── sheet 行 ── */
.info-sheet-trigger {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 100%;
  min-height: 26px;
  padding: 2px 0;
  border: none;
  background: none;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  text-align: left;
  cursor: pointer;
}
.info-sheet-trigger span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.trigger-arrow { margin-left: auto; flex-shrink: 0; }

.danger-item { color: var(--el-color-danger); }
</style>

<style>
/* ⋯ 弹出层 teleport 到 body，scoped 无法命中 */
.dev-head-more { max-width: calc(100vw - 24px); box-sizing: border-box; }
</style>
