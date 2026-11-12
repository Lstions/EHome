<!--
  顶栏中段的全局快速跳转搜索框（输入框 + ⌘K/Ctrl+K 提示 + 回车跳转）。

  为什么切成子组件：它是顶栏三段里**唯一**自包含的一段 —— 「一个输入值 + 一张
  关键字→路由的映射表」，不碰任何 store，也不参与侧栏/抽屉的键盘与焦点协议。

  边界依据（为什么不连外层 .header-center 一起搬）：窄屏隐藏搜索框的两条断点规则
  （@media 768 / 769~1024 的 `.header-center { display: none }`）写在页面里，
  由 MainLayoutHeaderCollapse.spec 的「④」直接锁定；外层容器留在页面 ⇒ 断点规则
  命中的仍是页面自己的元素，不依赖「子组件根节点继承父作用域 id」这一隐式行为。
  Ctrl+K 快捷键（页面的 handleKeydown）用 `document.querySelector('.global-search input')`
  定位本组件，类名保持不变。
-->
<template>
  <el-input
    v-model="searchQuery"
    :placeholder="searchPlaceholder"
    aria-label="快速跳转"
    prefix-icon="Search"
    clearable
    class="global-search"
    @keyup.enter="handleSearch"
  >
    <template #suffix>
      <kbd class="search-kbd">{{ isMac ? '⌘K' : 'Ctrl+K' }}</kbd>
    </template>
  </el-input>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

const router = useRouter()

// 搜索
const searchQuery = ref('')

// 平台检测
const isMac = computed(() => /Mac/i.test(navigator.platform))

// 快速跳转提示
const searchPlaceholder = computed(() => '快速跳转：仪表盘、节点、设备、通道…')

// 搜索处理
const handleSearch = () => {
  const query = searchQuery.value.trim()
  if (!query) return

  // 顶栏仅承担页面快速跳转，资源检索由对应列表页完成。
  const lowerQuery = query.toLowerCase()
  const destinations = [
    { keywords: ['仪表盘', 'dashboard'], path: '/dashboard' },
    { keywords: ['节点', 'node'], path: '/node' },
    { keywords: ['边缘设备', '设备', 'device'], path: '/edge-device' },
    { keywords: ['通道', 'channel'], path: '/channel' },
    { keywords: ['数据', 'data'], path: '/data' },
    { keywords: ['固件', 'firmware'], path: '/firmware' },
    { keywords: ['配置', 'config'], path: '/device-configs' },
    { keywords: ['监控', 'monitor'], path: '/monitor' },
  ]
  const destination = destinations.find(({ keywords }) => keywords.some(keyword => lowerQuery.includes(keyword)))
  if (destination) router.push(destination.path)
  else ElMessage.info('未找到匹配页面，请输入仪表盘、节点、设备、通道、数据、固件、配置或监控')
  searchQuery.value = ''
}
</script>

<style scoped>
.global-search {
  width: 100%;
}

/* 选择器从页面的 `:deep(.global-search …)` 改写为 `.global-search :deep(…)`：
   本组件的根节点**就是** .global-search（el-input），前者会编译成
   `[data-v-x] .global-search …`，要求 .global-search 是后代，永远不匹配。 */
.global-search :deep(.el-input__wrapper) {
  border-radius: 20px;
  background: var(--el-fill-color-light);
  box-shadow: none;
  border: 1px solid transparent;
  transition: all 0.3s;
}

.global-search :deep(.el-input__wrapper:hover),
.global-search :deep(.el-input__wrapper.is-focus) {
  border-color: var(--el-color-primary);
  background: var(--bg-color-overlay);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-primary) 12%, transparent);
}

.search-kbd {
  display: inline-block;
  padding: 2px 6px;
  font-size: 11px;
  font-family: inherit;
  background: var(--bg-color-overlay);
  border: 1px solid var(--border-color);
  border-radius: 4px;
  color: var(--el-text-color-secondary);
}
</style>
