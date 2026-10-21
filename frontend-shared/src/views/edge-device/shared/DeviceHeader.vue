<template>
  <!--
    设备详情页统一页头（Inverter / BMS / Generic 三处共用，2026-10-21 统一）。

    ⚠ 为什么不再复用 PageHeader：
      旧版 PageHeader 是「左标题 + 右操作」的单行 flex，393px 下靠 flex 挤压勉强放得下；
      用户真机（浏览器字体放大/窄屏）实测：标题被压到 16px 宽折成 3 行（int / er），
      「⋯」按钮掉到第三行、按钮高度被压到 28px（低于项目 36px 触控档）。
      手机页头必须**确定性两行**：第一行 身份（返回/名称/状态/⋯），第二行 主操作（刷新）。
  -->
  <header class="device-header">
    <!-- 可选面包屑（BMS 页保留「边缘设备管理 / BMS 设备详情」上下文；其它设备页不传） -->
    <nav v-if="breadcrumb && breadcrumb.length" class="dh-crumb" aria-label="面包屑">
      <template v-for="(item, i) in breadcrumb" :key="i">
        <span v-if="i > 0" class="dh-sep" aria-hidden="true">/</span>
        <button v-if="item.to" type="button" class="dh-crumb-link" @click="goCrumb(item.to)">{{ item.text }}</button>
        <span v-else class="dh-crumb-current">{{ item.text }}</span>
      </template>
    </nav>

    <div class="dh-main">
      <el-button class="dh-back" :icon="ArrowLeft" circle aria-label="返回上一页" @click="$emit('back')" />

      <div class="dh-ident">
        <div class="dh-name-line">
          <h2 class="dh-name" :title="title">{{ title }}</h2>
          <div class="dh-tags">
            <!--
              实时连接标签**不放在身份行**：实测 BMS 页身份行已有 2 个标签（在线 / 类型·16S），
              再加一个会折成两行、把窄屏页头从 103px 顶到 212px（占 851 视口 25%）。
              移动端统一放到下方操作行的左侧（与「刷新数据」同一行）。
            -->
            <el-tag
              v-for="(t, i) in tags"
              :key="i"
              :type="t.type || 'info'"
              :effect="t.effect || 'plain'"
              size="small"
            >{{ t.text }}</el-tag>
          </div>
        </div>
        <div v-if="subtitle || $slots.subtitle" class="dh-sub">
          <slot name="subtitle">{{ subtitle }}</slot>
        </div>
      </div>

      <!-- 桌面：完整操作组（编辑 / 同步 / 刷新 / 删除） -->
      <div v-if="!isMobile" class="dh-actions">
        <el-tag v-if="wsConnected" type="success" size="small" effect="plain" class="dh-conn-tag">
          <el-icon :size="12"><Connection /></el-icon>
          <span>实时连接</span>
        </el-tag>
        <el-button v-if="showEdit" :icon="Edit" @click="onEdit">编辑</el-button>
        <el-button v-if="showSync" :icon="Upload" :loading="syncingHA" :disabled="!canOperate" @click="$emit('syncToHA')">
          同步到 HA
        </el-button>
        <el-button v-if="showRefresh" type="primary" :icon="Refresh" :loading="refreshing" @click="$emit('refresh')">
          刷新数据
        </el-button>
        <el-button v-if="showDelete" type="danger" text :icon="Delete" :aria-label="`删除 ${title}`" @click="onDelete">删除</el-button>
      </div>

    </div>

    <!-- 移动：主操作独占一行（不做 flex 挤压，任何宽度都不掉行/不缩小） -->
    <div v-if="isMobile" class="dh-mobile-actions">
      <el-tag v-if="wsConnected" type="success" size="small" effect="plain" class="dh-conn-tag dh-conn-mobile">
        <el-icon :size="12"><Connection /></el-icon>
        <span>实时连接</span>
      </el-tag>
      <el-button
        v-if="showRefresh"
        type="primary"
        :icon="Refresh"
        :loading="refreshing"
        class="dh-refresh"
        @click="$emit('refresh')"
      >刷新数据</el-button>
      <!-- ⋯ 溢出菜单（编辑 / 同步 / 删除）：放在操作行而非身份行。
           2026-10-21 独立复核实测：⋯ 占 44+10px 后身份行只剩 152px（320 视口），
           导致 BMS 的 2 个标签折 2 行、副文本折 2 行，页头从 103px 涨到 202px。
           移到操作行后身份行拿回 54px，标签与副文本各少折一行。 -->
      <el-dropdown v-if="showMoreMenu" trigger="click" popper-class="device-header-more-popover" @command="handleMoreCommand">
        <el-button class="dh-more" :icon="MoreFilled" circle aria-label="更多设备操作" />
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item v-if="showEdit" command="edit" :icon="Edit">编辑</el-dropdown-item>
            <el-dropdown-item v-if="showSync" command="syncToHA" :icon="Upload" :disabled="!canOperate">同步到 HA</el-dropdown-item>
            <el-dropdown-item v-if="showDelete" command="delete" :icon="Delete" divided>
              <span class="dh-danger">删除</span>
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <!-- 内置编辑/删除对话框（保留旧行为；BMS 页传 :builtin-dialogs="false" 用自己的对话框） -->
    <el-dialog v-if="builtinDialogs" v-model="editDialogVisible" title="编辑边缘设备" width="500px">
      <el-form :model="editForm" label-width="80px">
        <el-form-item label="设备名称">
          <el-input v-model="editForm.name" />
        </el-form-item>
        <el-form-item label="设备类型">
          <el-input :model-value="device?.device_type" disabled />
        </el-form-item>
        <el-form-item label="通信协议">
          <el-input :model-value="device?.protocol?.toUpperCase()" disabled />
        </el-form-item>
        <el-form-item label="硬件类型">
          <el-input :model-value="device?.hardware_type ? getHardwareLabel(device.hardware_type) : ''" disabled />
        </el-form-item>
        <el-form-item label="硬件ID">
          <el-input :model-value="device?.hardware_id" disabled />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="editLoading" @click="submitEdit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-if="builtinDialogs" v-model="deleteDialogVisible" title="删除边缘设备" width="400px">
      <p class="dh-delete-tip">确定要删除边缘设备 <strong>{{ device?.name }}</strong> 吗？此操作不可恢复。</p>
      <template #footer>
        <el-button @click="deleteDialogVisible = false">取消</el-button>
        <el-button type="danger" :loading="deleteLoading" @click="submitDelete">删除</el-button>
      </template>
    </el-dialog>
  </header>
</template>
<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { feedback } from '@/utils/feedback'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowLeft, Refresh, Connection, Edit, Upload, Delete, MoreFilled } from '@element-plus/icons-vue'
import { useResponsive } from '@/composables/useResponsive'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import { getHardwareLabel } from '@/utils/hardwareTag'

export interface DeviceHeaderTag {
  text: string
  type?: 'success' | 'info' | 'warning' | 'danger' | 'primary'
  effect?: 'dark' | 'light' | 'plain'
}

const props = withDefaults(defineProps<{
  device: EdgeDevice | null
  wsConnected?: boolean
  syncingHA?: boolean
  refreshing?: boolean
  title?: string
  /** 身份行副标题（BMS 的设备元信息行）；也可用 #subtitle 插槽传富文本 */
  subtitle?: string
  /** 名称行尾部的状态标签（BMS 的「在线 / jiabaida_bms · 16S」） */
  tags?: DeviceHeaderTag[]
  /** 面包屑（BMS 用；其它设备页不传） */
  breadcrumb?: Array<{ text: string; to?: string }>
  /** 是否使用组件内置的编辑/删除对话框（BMS 用自己的对话框，传 false） */
  builtinDialogs?: boolean
  /** 操作可见性：默认全开；BMS 页按自己的权限/状态控制 */
  showEdit?: boolean
  showSync?: boolean
  showRefresh?: boolean
  showDelete?: boolean
}>(), {
  wsConnected: false,
  syncingHA: false,
  refreshing: false,
  title: '',
  subtitle: '',
  tags: () => [],
  breadcrumb: () => [],
  builtinDialogs: true,
  showEdit: true,
  showSync: true,
  showRefresh: true,
  showDelete: true,
})

const emit = defineEmits<{
  (e: 'back'): void
  (e: 'syncToHA'): void
  (e: 'refresh'): void
  (e: 'edit'): void
  (e: 'delete'): void
  (e: 'updated'): void
  (e: 'deleted'): void
}>()

const router = useRouter()
const edgeDeviceStore = useEdgeDeviceStore()
const { isMobile } = useResponsive()

/** 设备可操作性：仅在线/激活设备允许同步与删除语义上的写操作 */
const canOperate = computed(() => {
  const status = props.device?.status
  return status === 'online' || status === 'active'
})

/** 移动端是否有任何要收进 ⋯ 的次级操作 */
const showMoreMenu = computed(() => props.showEdit || props.showSync || props.showDelete)

const editDialogVisible = ref(false)
const editLoading = ref(false)
const editForm = ref({ name: '' })
const deleteDialogVisible = ref(false)
const deleteLoading = ref(false)
let operationGeneration = 0

watch(editDialogVisible, (v) => {
  if (v) editForm.value.name = props.device?.name || ''
})

/** 编辑入口：内置对话框模式直接打开；否则交给父页（BMS 有自己的编辑弹窗） */
function onEdit() {
  if (props.builtinDialogs) editDialogVisible.value = true
  else emit('edit')
}

function onDelete() {
  if (props.builtinDialogs) deleteDialogVisible.value = true
  else emit('delete')
}

function goCrumb(to: string) {
  router.push(to)
}

async function submitEdit() {
  if (!props.device) return
  const deviceId = props.device.id
  const operation = operationGeneration
  const generation = getSessionGeneration()
  editLoading.value = true
  try {
    await edgeDeviceApi.update(deviceId, { name: editForm.value.name })
    if (operation !== operationGeneration) return
    assertSessionGeneration(generation)
    if (props.device?.id !== deviceId) throw new Error('设备已变更')
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(deviceId)
    ElMessage.success('设备信息已保存')
    editDialogVisible.value = false
    emit('updated')
  } catch (e: any) {
    if (operation !== operationGeneration || props.device?.id !== deviceId) return
    feedback.handleError(e, '保存失败')
  } finally {
    if (props.device?.id === deviceId) editLoading.value = false
  }
}

async function submitDelete() {
  if (!props.device) return
  const deviceId = props.device.id
  const operation = operationGeneration
  deleteLoading.value = true
  try {
    await edgeDeviceStore.deleteDevice(deviceId)
    if (operation !== operationGeneration) return
    if (props.device?.id !== deviceId) throw new Error('设备已变更')
    ElMessage.success('设备已删除')
    deleteDialogVisible.value = false
    emit('deleted')
    router.replace('/edge-device')
  } catch (e: any) {
    if (operation !== operationGeneration || props.device?.id !== deviceId) return
    feedback.handleError(e, '删除失败')
  } finally {
    if (props.device?.id === deviceId) deleteLoading.value = false
  }
}

function handleMoreCommand(command: string) {
  if (command === 'edit') onEdit()
  else if (command === 'syncToHA') emit('syncToHA')
  else if (command === 'delete') onDelete()
}

onUnmounted(() => {
  operationGeneration++
})
</script>
<style scoped>
.device-header {
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color);
  padding: 12px 24px 12px;
  margin-bottom: 16px;
}

/* 面包屑（可点击段必须是原生 button 且清空 UA 样式，否则手机/桌面都会出现老式按钮灰底黑边） */
.dh-crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.dh-crumb-link {
  appearance: none;
  -webkit-appearance: none;
  border: 0;
  background: none;
  padding: 0;
  margin: 0;
  font: inherit;
  color: inherit;
  cursor: pointer;
}
.dh-crumb-link:hover { color: var(--el-color-primary); }
.dh-crumb-link:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 2px; border-radius: 2px; }
.dh-crumb-current { color: var(--el-text-color-primary); }
.dh-sep { color: var(--el-border-color); }

.dh-main {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.dh-ident { min-width: 0; flex: 1 1 auto; }
.dh-name-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  row-gap: 6px;
  min-width: 0;
}
.dh-name {
  margin: 0;
  font-size: 20px;
  font-weight: 500;
  line-height: 1.3;
  color: var(--el-text-color-primary);
  /* 设备名是身份信息：**不允许按字符折断**（旧版 -webkit-line-clamp + word-break:break-word
     在 320px 上把 inter 拆成 int/er），只在必要时按词换行 */
  word-break: keep-all;
  overflow-wrap: break-word;
  min-width: 0;
}
.dh-tags {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-width: 0;
}
.dh-conn-tag :deep(.el-tag__content) { display: inline-flex; align-items: center; gap: 4px; }
.dh-sub {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
  word-break: keep-all;
  overflow-wrap: break-word;
}
.dh-sub :deep(b) { color: var(--el-text-color-primary); }

.dh-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  justify-content: flex-end;
  flex: 0 0 auto;
}
.dh-more {
  width: 36px;
  height: 36px;
  flex: 0 0 auto;
}
.dh-danger { color: var(--el-color-danger); }
.dh-delete-tip { margin: 0; }

.dh-mobile-actions { display: none; }

/*
  窄屏（≤768px）：**确定性两行**布局。
    行 1：返回 + 名称/标签 + ⋯
    行 2：主操作（刷新数据）独占整行
  这样在 320/360/393 以及系统字体被放大的真机上都不会出现「标题被挤成 3 行、按钮掉行」。
*/
@media (max-width: 768px) {
  .device-header { padding: 10px 16px 12px; margin-bottom: 12px; }
  .dh-crumb { margin-bottom: 6px; font-size: 11px; }
  .dh-main { gap: 10px; }
  .dh-name { font-size: 18px; }
  .dh-actions { display: none; }
  .dh-mobile-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
  }
  /* 实时连接标签与刷新按钮同行：固定不参与 flex 拉伸，按钮吃掉剩余宽度 */
  .dh-conn-mobile {
    flex: 0 0 auto;
    height: 36px;
    padding: 0 10px;
    font-size: 12px;
  }
  .dh-refresh {
    flex: 1 1 auto;
    /* 主操作固定 36px 档（项目窄屏行内按钮档位），不再被压成 28px */
    height: 36px;
  }
  .dh-sub { margin-top: 4px; }
}

/* 粗指针（含触屏笔记本）：⋯ 与刷新按钮升到 44px 触摸目标 */
@media (pointer: coarse) {
  .dh-more { width: 44px; height: 44px; }
  .dh-refresh { min-height: 44px; }
}

/* el-dropdown 弹出层 teleport 到 body，scoped 样式不生效，需 :global 包裹 */
:global(.device-header-more-popover) {
  max-width: calc(100vw - 24px);
  box-sizing: border-box;
}
</style>