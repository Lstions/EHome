<template>
  <!-- notes 为空 ⇒ 整个弹窗都不渲染（§6.4 / AC-18：不出现只有版本号的空弹窗）。
       数据只有一处来源：store 里的 notes（来自 GET /system/version 的 data.notes）。 -->
  <el-dialog
    v-if="store.hasReleaseNotes"
    v-model="visible"
    title="关于与更新内容"
    width="560px"
    align-center
    data-testid="release-notes-dialog"
    class="release-notes-dialog"
  >
    <div class="release-notes" data-testid="release-notes-body">
      <dl class="release-meta">
        <div class="release-meta-item">
          <dt>版本</dt>
          <dd>v{{ store.displayVersion }}</dd>
        </div>
        <div class="release-meta-item">
          <dt>构建</dt>
          <dd class="mono">{{ store.displayBuildId }}</dd>
        </div>
        <!-- 仅当服务端 build 与当前页面不同（stale / blocked）时补一行：
             否则「关于」里只有一个 build，用户无法判断自己差在哪 -->
        <div v-if="serverBuildDiffers" class="release-meta-item" data-testid="release-meta-server-build">
          <dt>服务端构建</dt>
          <dd class="mono">{{ store.serverBuildId }}</dd>
        </div>
        <div v-if="buildTimeText" class="release-meta-item">
          <dt>构建时间</dt>
          <dd>{{ buildTimeText }}</dd>
        </div>
      </dl>

      <p class="release-notes-title">本次更新内容</p>
      <ul class="release-notes-list">
        <li
          v-for="(note, index) in store.notes"
          :key="index"
          class="release-note-item"
          data-testid="release-note-item"
        >{{ note }}</li>
      </ul>
    </div>

    <template #footer>
      <el-button data-testid="release-notes-close" @click="close">关闭</el-button>
      <!-- 只读弹窗；仅在「页面确实过期 / 已阻断」时追加刷新入口 -->
      <el-button
        v-if="canRefresh"
        type="primary"
        data-testid="release-notes-refresh"
        @click="store.refreshNow()"
      >立即刷新</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useAppUpdateStore } from '@/stores/appUpdate'

/**
 * 「更新内容」弹窗（契约 §6.4 A 档 / T2.12）。
 *
 * 两个入口**共用同一个组件实例**（都在 App 层由 UpdateBanner 挂载）：
 *   ① stale 横幅上的「查看更新内容」；
 *   ② 侧栏底部 / 移动端抽屉里的版本号（「关于」）。
 * 因此本组件**不接收 notes prop**，只读 store —— 数据只有一处来源，
 * 两个入口不可能显示不同的内容。
 *
 * 打开/关闭弹窗**不改变**更新状态、不触发重试；blocked 时弹窗可关，但模态仍在。
 * 按钮：「关闭」恒定；stale / blocked 下追加「立即刷新」（只读语义的例外，
 * 因为此时用户最想做的事就是刷新，不该让他先关弹窗再去找横幅）。
 */
const store = useAppUpdateStore()

const visible = computed({
  get: () => store.releaseNotesVisible,
  set: (value: boolean) => {
    if (value) store.openReleaseNotes()
    else store.closeReleaseNotes()
  },
})

const canRefresh = computed(() => store.state === 'stale' || store.state === 'blocked')

/** 服务端 build 与当前页面 build 是否不同（stale / blocked 时为 true）。 */
const serverBuildDiffers = computed(() => {
  const server = store.serverBuildId
  return Boolean(server) && server !== store.displayBuildId
})

/** build_time 原样展示（ISO 串），解析失败则整行不出现，而不是显示 Invalid Date。 */
const buildTimeText = computed(() => {
  const raw = store.serverBuildTime
  if (!raw) return ''
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString('zh-CN')
})

function close() {
  store.closeReleaseNotes()
}
</script>

<style scoped>
.release-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 24px;
  margin: 0 0 16px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
}
.release-meta-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
  margin: 0;
}
.release-meta dt {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.release-meta dd {
  margin: 0;
  font-size: 13px;
  color: var(--el-text-color-primary);
}
.release-meta .mono {
  font-family: var(--el-font-family-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  word-break: break-all;
}
.release-notes-title {
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.release-notes-list {
  margin: 0;
  padding-left: 18px;
  max-height: 40vh;
  overflow-y: auto;
}
.release-note-item {
  margin: 0 0 6px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
  word-break: break-word;
}
</style>
