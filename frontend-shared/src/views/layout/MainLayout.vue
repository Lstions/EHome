<template>
  <el-container class="main-layout">
    <!-- 侧边栏 (桌面端) -->
    <el-aside v-if="!isMobile" :width="sidebarWidth" class="sidebar">
      <!-- Logo 区域 -->
      <div class="logo-area" @click="router.push('/dashboard')">
        <div class="logo-icon">
          <img src="/favicon.svg" alt="EHomeSystem" style="width: 24px; height: 24px;" />
        </div>
        <transition name="fade">
          <span v-if="!uiStore.sidebarCollapsed" class="logo-text">EHomeSystem</span>
        </transition>
      </div>

      <!-- 导航菜单 -->
      <el-menu
        :default-active="activeMenu"
        :collapse="uiStore.sidebarCollapsed"
        :collapse-transition="false"
        router
        class="sidebar-menu"
      >
        <el-menu-item v-for="item in menuItems" :key="item.path" :index="item.path">
          <el-icon><component :is="item.icon" /></el-icon>
          <template #title>{{ item.title }}</template>
        </el-menu-item>
      </el-menu>

      <!-- 侧边栏底部 -->
      <div class="sidebar-footer">
        <div class="version-info" v-if="!uiStore.sidebarCollapsed">
          <span>v{{ appVersion }}</span>
        </div>
      </div>
    </el-aside>

    <!-- 侧边栏 (移动端抽层) -->
    <el-drawer
      v-if="isMobile"
      v-model="mobileDrawerVisible"
      direction="ltr"
      :with-header="false"
      size="240px"
      class="mobile-sidebar-drawer"
    >
      <div class="logo-area" @click="router.push('/dashboard')">
        <div class="logo-icon">
          <img src="/favicon.svg" alt="EHomeSystem" style="width: 24px; height: 24px;" />
        </div>
        <span class="logo-text">EHomeSystem</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        router
        class="sidebar-menu"
        @select="mobileDrawerVisible = false"
      >
        <el-menu-item v-for="item in menuItems" :key="item.path" :index="item.path">
          <el-icon><component :is="item.icon" /></el-icon>
          <template #title>{{ item.title }}</template>
        </el-menu-item>
      </el-menu>
    </el-drawer>

    <!-- 右侧容器 -->
    <el-container class="main-container">
      <!-- 顶部 Header -->
      <el-header class="main-header">
        <!-- 左侧：折叠按钮 + 面包屑 -->
        <div class="header-left">
          <!-- 移动端用汉堡按钮 + 抽屉 -->
          <el-button
            v-if="isMobile"
            :icon="Menu"
            circle
            size="default"
            class="collapse-btn"
            aria-label="打开导航菜单"
            @click="mobileDrawerVisible = true"
          />
          <!-- 桌面端用折叠按钮 -->
          <el-button
            v-else
            :icon="Fold"
            circle
            size="default"
            class="collapse-btn"
            aria-label="折叠侧边导航"
            @click="uiStore.toggleSidebar"
          />
          
          <el-breadcrumb separator="/" class="breadcrumb">
            <el-breadcrumb-item :to="{ path: '/dashboard' }">
              <el-icon><HomeFilled /></el-icon>
            </el-breadcrumb-item>
            <el-breadcrumb-item v-for="(item, index) in breadcrumbs" :key="index">
              {{ item }}
            </el-breadcrumb-item>
          </el-breadcrumb>
        </div>

        <!-- 中间：全局搜索 -->
        <div class="header-center">
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
        </div>

        <!-- 右侧：通知 + 主题 + 用户 -->
        <div class="header-right">
          <!-- WebSocket 状态 -->
          <div class="ws-status" :class="{ connected: wsStore.connected }">
            <span class="status-dot"></span>
            <span class="status-text">{{ wsStore.connected ? '在线' : '离线' }}</span>
          </div>

          <!-- 通知铃铛 -->
          <el-popover placement="bottom-end" :width="320" trigger="click">
            <template #reference>
              <el-badge :value="notificationCount" :hidden="notificationCount === 0" class="notification-badge">
                <el-button :icon="Bell" circle size="default" aria-label="打开通知中心" />
              </el-badge>
            </template>
            <div class="notification-panel">
              <div class="notification-header">
                <span>通知中心</span>
                <el-button link type="primary" size="small" @click="clearNotifications">全部已读</el-button>
              </div>
              <el-scrollbar max-height="300px">
                <div v-if="notifications.length === 0" class="notification-empty">
                  <el-icon :size="32"><Bell /></el-icon>
                  <p>暂无新通知</p>
                </div>
                <div v-else class="notification-list">
                  <div 
                    v-for="item in notifications" 
                    :key="item.id" 
                    class="notification-item"
                    :class="{ unread: !item.read }"
                    @click="handleNotificationClick(item)"
                  >
                    <div class="notification-icon" :class="item.type">
                      <el-icon><WarningFilled v-if="item.type === 'warning'" /><SuccessFilled v-else-if="item.type === 'success'" /><InfoFilled v-else /></el-icon>
                    </div>
                    <div class="notification-content">
                      <p class="notification-title">{{ item.title }}</p>
                      <p class="notification-desc">{{ item.description }}</p>
                      <span class="notification-time">{{ item.created_at }}</span>
                    </div>
                  </div>
                </div>
              </el-scrollbar>
            </div>
          </el-popover>

          <!-- 主题切换 -->
          <ThemeSwitch />

          <!-- 用户菜单 -->
          <el-dropdown trigger="click" @command="handleCommand">
            <div class="user-menu">
              <el-avatar :size="32" class="user-avatar">
                {{ userStore.userInfo?.username?.charAt(0)?.toUpperCase() || 'U' }}
              </el-avatar>
              <span class="user-name">{{ userStore.userInfo?.username || 'User' }}</span>
              <el-icon><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item disabled>
                  <el-icon><User /></el-icon>
                  <span>{{ userStore.userInfo?.email || 'user@example.com' }}</span>
                </el-dropdown-item>
                <el-dropdown-item divided command="profile">
                  <el-icon><UserFilled /></el-icon>
                  <span>个人设置</span>
                </el-dropdown-item>
                <el-dropdown-item v-if="userStore.isAdmin" command="users">
                  <el-icon><UserFilled /></el-icon>
                  <span>用户管理</span>
                </el-dropdown-item>
                <el-dropdown-item divided command="logout">
                  <el-icon><SwitchButton /></el-icon>
                  <span>退出登录</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <!-- 主内容区 -->
      <el-main class="main-content">
        <router-view v-slot="{ Component }">
          <transition name="fade-slide" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useResponsive } from '@/composables/useResponsive'
import { useRouter, useRoute } from 'vue-router'
import {
  Odometer,
  Connection,
  Cpu,
  DataLine,
  Files,
  Setting,
  Fold,
  ArrowDown,
  DataAnalysis,
  HomeFilled,
  Bell,
  User,
  UserFilled,
  SwitchButton,
  WarningFilled,
  SuccessFilled,
  InfoFilled,
  Search,
  Menu,
} from '@element-plus/icons-vue'
import { useUserStore } from '@/stores/user'
import { useUIStore } from '@/stores/ui'
import { useWebSocketStore } from '@/stores/websocket'
import { getNotifications, getUnreadCount, markAsRead, markAllAsRead, type Notification as ApiNotification } from '@/api/notification'
import { ElMessage } from 'element-plus'
import ThemeSwitch from '@/components/common/ThemeSwitch.vue'
import feedback from '@/utils/feedback'
import { logger } from '@/utils/logger'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const uiStore = useUIStore()
const wsStore = useWebSocketStore()

// 搜索
const searchQuery = ref('')

// 平台检测
const isMac = computed(() => /Mac/i.test(navigator.platform))

// 响应式
const { isMobile } = useResponsive()
const mobileDrawerVisible = ref(false)

// 菜单项（全部，含角色要求）
const allMenuItems = [
  { path: '/dashboard', title: '仪表盘', icon: Odometer, roles: undefined },
  { path: '/node', title: '节点', icon: Connection, roles: undefined },
  { path: '/edge-device', title: '边缘设备', icon: Cpu, roles: undefined },
  { path: '/channel', title: '通道管理', icon: Connection, roles: undefined },
  { path: '/data', title: '数据面板', icon: DataLine, roles: undefined },
  { path: '/firmware', title: '固件管理', icon: Files, roles: ['admin', 'operator'] as string[] },
  { path: '/device-configs', title: '配置模板', icon: Setting, roles: ['admin', 'operator'] as string[] },
  { path: '/monitor', title: '系统监控', icon: DataAnalysis, roles: ['admin'] as string[] },
]

// 根据角色过滤菜单
const menuItems = computed(() => {
  const role = userStore.role
  return allMenuItems.filter((item) => {
    if (!item.roles) return true
    return item.roles.includes(role)
  })
})

const appVersion = computed(() => import.meta.env.VITE_APP_VERSION || '2.2.0')

// 通知 - 从 API 获取
const notifications = ref<ApiNotification[]>([])
const notificationCount = ref(0)

const fetchNotifications = async () => {
  try {
    const [notifs, count] = await Promise.all([
      getNotifications(20),
      getUnreadCount()
    ])
    notifications.value = notifs
    notificationCount.value = count
  } catch (error) {
    // API 失败时显示空列表，不报错
    notifications.value = []
    notificationCount.value = 0
  }
}

// 初始化获取通知
fetchNotifications()

// 侧边栏宽度
const sidebarWidth = computed(() => {
  return uiStore.sidebarCollapsed ? '64px' : '200px'
})

// 当前激活菜单
const activeMenu = computed(() => {
  return route.path
})

// 面包屑
const breadcrumbs = computed(() => {
  const pathNames: Record<string, string> = {
    '/dashboard': '仪表盘',
    '/node': '节点管理',
    '/edge-device': '边缘设备管理',
    '/channel': '通道管理',
    '/data': '数据面板',
    '/firmware': '固件管理',
    '/device-configs': '配置模板',
    '/monitor': '系统监控',

    '/node/:id': '节点详情',
    '/edge-device/:id': '边缘设备详情',
  }

  const crumbs: string[] = []
  const path = route.path
  
  // 匹配一级路径
  for (const [key, value] of Object.entries(pathNames)) {
    if (path.startsWith(key.replace('/:id', '')) && key !== '/dashboard') {
      crumbs.push(value)
      break
    }
  }

  return crumbs
})

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

// 通知处理
const clearNotifications = async () => {
  try {
    await markAllAsRead()
    await fetchNotifications()
    feedback.success('已全部标记为已读')
  } catch (error) {
    feedback.handleError(error, '标记已读失败')
  }
}

const handleNotificationClick = async (item: ApiNotification) => {
  try {
    await markAsRead(item.id)
    item.read = true
    notificationCount.value = Math.max(0, notificationCount.value - 1)
  } catch (error) {
    // 静默失败
  }
  // 根据通知类型跳转
  if (item.title.includes('离线')) {
    router.push('/edge-device')
  }
}

// 用户菜单操作
const handleCommand = async (command: string) => {
  if (command === 'logout') {
    const ok = await feedback.confirmDanger('确定要退出登录吗？', {
      title: '退出登录',
      confirmText: '退出',
    })
    if (!ok) return
    wsStore.disconnect()
    await userStore.logout()
    feedback.success('已退出登录')
    router.push('/login')
  } else if (command === 'profile') {
    router.push('/profile')
  } else if (command === 'users') {
    router.push('/admin/users')
  }
}

// 键盘快捷键
const handleKeydown = (e: KeyboardEvent) => {
  if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
    e.preventDefault()
    document.querySelector<HTMLInputElement>('.global-search input')?.focus()
  }
}

onMounted(() => {
  if (wsStore.isAuthenticated) {
    logger.debug('[MainLayout] 已登录，连接 WebSocket')
    wsStore.connect()
  } else {
    logger.debug('[MainLayout] 未登录，跳过 WebSocket')
  }
  document.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  wsStore.disconnect()
  document.removeEventListener('keydown', handleKeydown)
})
</script>

<style scoped>
.main-layout {
  height: 100vh;
  overflow: hidden;
}

/* ========== 侧边栏 ========== */
.sidebar {
  background: linear-gradient(180deg, #1a1f2e 0%, #1e2538 100%);
  display: flex;
  flex-direction: column;
  transition: width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}

.logo-area {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  cursor: pointer;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
  transition: all 0.3s;
}

.logo-area:hover {
  background: rgba(255, 255, 255, 0.04);
}

.logo-icon {
  width: 36px;
  height: 36px;
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
}

.logo-text {
  font-size: 18px;
  font-weight: 600;
  color: #fff;
  white-space: nowrap;
}

.sidebar-menu {
  flex: 1;
  border-right: none;
  background: transparent;
  padding: 8px;
}

:deep(.el-menu-item) {
  height: 44px;
  margin: 2px 0;
  border-radius: 8px;
  color: rgba(255, 255, 255, 0.65);
  transition: all 0.3s;
}

:deep(.el-menu-item:hover) {
  background: rgba(255, 255, 255, 0.08);
  color: #fff;
}

:deep(.el-menu-item.is-active) {
  background: linear-gradient(90deg, rgba(64, 158, 255, 0.2) 0%, rgba(64, 158, 255, 0.1) 100%);
  color: var(--el-color-primary);
}

:deep(.el-menu-item.is-active)::before {
  content: '';
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 20px;
  background: var(--el-color-primary);
  border-radius: 0 3px 3px 0;
}

.sidebar-footer {
  padding: 12px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
}

.version-info {
  text-align: center;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.3);
}

/* ========== 右侧容器 ========== */
.main-container {
  background: var(--el-fill-color-light);
  overflow: hidden;
}

/* ========== Header ========== */
.main-header {
  height: 60px;
  background: #fff;
  border-bottom: 1px solid #e8eaec;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
  z-index: 100;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.collapse-btn {
  background: var(--el-fill-color-light);
  border: none;
  transition: all 0.3s;
}

.collapse-btn:hover {
  background: #e8eaec;
  transform: scale(1.05);
}

.breadcrumb {
  font-size: 14px;
}

:deep(.el-breadcrumb__item) {
  display: flex;
  align-items: center;
}

.header-center {
  flex: 1;
  max-width: 480px;
  margin: 0 24px;
}

.global-search {
  width: 100%;
}

:deep(.global-search .el-input__wrapper) {
  border-radius: 20px;
  background: var(--el-fill-color-light);
  box-shadow: none;
  border: 1px solid transparent;
  transition: all 0.3s;
}

:deep(.global-search .el-input__wrapper:hover),
:deep(.global-search .el-input__wrapper.is-focus) {
  border-color: var(--el-color-primary);
  background: #fff;
  box-shadow: 0 0 0 3px rgba(64, 158, 255, 0.1);
}

.search-kbd {
  display: inline-block;
  padding: 2px 6px;
  font-size: 11px;
  font-family: inherit;
  background: #fff;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  color: var(--el-text-color-secondary);
}

.header-right {
  display: flex;
  align-items: center;
  gap: 16px;
}

.ws-status {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  background: #fef0f0;
  border-radius: 20px;
  font-size: 12px;
  color: var(--el-color-danger);
  transition: all 0.3s;
}

.ws-status.connected {
  background: #f0f9eb;
  color: var(--el-color-success);
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  animation: pulse 2s infinite;
}

@keyframes pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.6; transform: scale(1.2); }
}

.notification-badge :deep(.el-badge__content) {
  background: var(--el-color-danger);
}

.notification-panel {
  margin: -12px;
}

.notification-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid #e8eaec;
  font-weight: 500;
}

.notification-empty {
  padding: 40px 20px;
  text-align: center;
  color: var(--el-text-color-secondary);
}

.notification-empty p {
  margin-top: 8px;
}

.notification-item {
  display: flex;
  gap: 12px;
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.2s;
}

.notification-item:hover {
  background: var(--el-fill-color-light);
}

.notification-item.unread {
  background: #ecf5ff;
}

.notification-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.notification-icon.info {
  background: #ecf5ff;
  color: var(--el-color-primary);
}

.notification-icon.warning {
  background: #fef0f0;
  color: var(--el-color-warning);
}

.notification-icon.success {
  background: #f0f9eb;
  color: var(--el-color-success);
}

.notification-content {
  flex: 1;
  min-width: 0;
}

.notification-title {
  margin: 0;
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}

.notification-desc {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.notification-time {
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}

.user-menu {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 8px;
  transition: background 0.2s;
}

.user-menu:hover {
  background: var(--el-fill-color-light);
}

.user-avatar {
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
  color: #fff;
  font-weight: 500;
}

.user-name {
  font-size: 14px;
  color: #303133;
}

/* ========== 主内容区 ========== */
.main-content {
  padding: 20px;
  overflow-y: auto;
  height: calc(100vh - 60px);
  background: var(--el-fill-color-light);
}

/* ========== 过渡动画 ========== */
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

.fade-slide-enter-active,
.fade-slide-leave-active {
  transition: all 0.25s ease;
}

.fade-slide-enter-from {
  opacity: 0;
  transform: translateY(10px);
}

.fade-slide-leave-to {
  opacity: 0;
  transform: translateY(-10px);
}

/* ========== 响应式 ========== */
@media (max-width: 768px) {
  .header-center {
    display: none;
  }
  
  .breadcrumb {
    display: none;
  }
  
  .ws-status .status-text {
    display: none;
  }
  
  .user-name {
    display: none;
  }
}

/* 平板 */
@media (min-width: 769px) and (max-width: 1024px) {
  .logo-text {
    display: none;
  }
  
  .global-search {
    max-width: 240px;
  }
}

/* 大屏 */
@media (min-width: 1536px) {
  .main-content {
    padding: 24px 32px;
  }
}
</style>
