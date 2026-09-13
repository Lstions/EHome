<template>
  <el-container class="main-layout">
    <!-- 侧边栏 (桌面端) -->
    <el-aside v-if="!isMobile" :width="sidebarWidth" class="sidebar">
      <!-- Logo 区域 -->
      <div class="logo-area" @click="router.push('/dashboard')">
        <div class="logo-icon">
          <img :src="withBase('/favicon.svg')" alt="EHomeSystem" style="width: 24px; height: 24px;" />
        </div>
        <transition name="fade">
          <span v-if="!uiStore.sidebarCollapsed" class="logo-text">EHomeSystem</span>
        </transition>
      </div>

      <!-- 导航菜单 -->
      <el-menu
        ref="desktopMenuRef"
        :default-active="activeMenu"
        :collapse="uiStore.sidebarCollapsed"
        :collapse-transition="false"
        router
        class="sidebar-menu"
        @keydown="handleSidebarKeydown"
      >
        <el-menu-item
          v-for="(item, idx) in menuItems"
          :key="item.path"
          :index="item.path"
          :data-index="item.path"
          :tabindex="idx === sidebarFocusIndex ? 0 : -1"
          :aria-current="activeMenu === item.path ? 'page' : undefined"
          @focus="sidebarFocusIndex = idx"
        >
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
      @opened="handleMobileDrawerOpened"
      @closed="handleMobileDrawerClosed"
    >
      <div ref="mobileDrawerBodyRef" class="mobile-drawer-body" tabindex="-1">
        <!-- Logo 区域 -->
        <div class="mobile-logo-area" @click="handleMobileLogoClick">
          <div class="logo-icon">
            <img :src="withBase('/favicon.svg')" alt="EHomeSystem" style="width: 24px; height: 24px;" />
          </div>
          <span class="mobile-logo-text">EHomeSystem</span>
        </div>

        <!-- 导航菜单 -->
        <el-menu
          ref="mobileMenuRef"
          :default-active="activeMenu"
          router
          class="mobile-sidebar-menu"
          @select="mobileDrawerVisible = false"
          @keydown="handleMobileMenuKeydown"
        >
          <!-- 抽屉是模态：所有导航项都可 Tab 到达，ElFocusTrap 才能在首尾之间循环。
               这里刻意不用 roving tabindex —— 那会只剩一个 tab 停靠点，Tab 会"卡"在同一项上。 -->
          <el-menu-item
            v-for="(item, idx) in menuItems"
            :key="item.path"
            :index="item.path"
            :data-index="item.path"
            :tabindex="0"
            :aria-current="activeMenu === item.path ? 'page' : undefined"
            @focus="mobileFocusIndex = idx"
          >
            <el-icon><component :is="item.icon" /></el-icon>
            <template #title>{{ item.title }}</template>
          </el-menu-item>
        </el-menu>

        <!-- 版本信息 -->
        <div class="mobile-sidebar-footer">
          <span class="mobile-version-info">v{{ appVersion }}</span>
        </div>
      </div>
    </el-drawer>

    <!-- 右侧容器 -->
    <el-container class="main-container">
      <!-- 顶部 Header -->
      <el-header class="main-header">
        <!-- 左侧：折叠按钮 + 面包屑 -->
        <div class="header-left">
          <!-- 移动端用汉堡按钮 + 抽屉 -->
          <el-button
            ref="mobileMenuTriggerRef"
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
          <el-popover placement="bottom-end" :width="320" trigger="click" popper-class="notification-popover">
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
          <component :is="Component" />
        </router-view>
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useResponsive } from '@/composables/useResponsive'
import { useRouter, useRoute } from 'vue-router'
import {
  Odometer,
  Connection,
  Cpu,
  Share,
  Link,
  DataLine,
  Files,
  Setting,
  Fold,
  ArrowDown,
  DataAnalysis,
  HomeFilled,
  Bell,
  SetUp,
  User,
  UserFilled,
  SwitchButton,
  WarningFilled,
  SuccessFilled,
  InfoFilled,
  Menu,
} from '@element-plus/icons-vue'
import { useUserStore } from '@/stores/user'
import { withBase } from '@/utils/basePath'
import { useUIStore } from '@/stores/ui'
import { useWebSocketStore } from '@/stores/websocket'
import { useNodeStore } from '@/stores/node'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { getNotifications, getUnreadCount, markAsRead, markAllAsRead, type Notification as ApiNotification } from '@/api/notification'
import { WS_EVENT } from '@/events/events'
import { ElMessage, ElNotification } from 'element-plus'
import ThemeSwitch from '@/components/common/ThemeSwitch.vue'
import feedback from '@/utils/feedback'
import { logger } from '@/utils/logger'
import { preloadPrimaryRoutes } from '@/router/routeLoaders'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const uiStore = useUIStore()
const wsStore = useWebSocketStore()
const nodeStore = useNodeStore()
const edgeDeviceStore = useEdgeDeviceStore()

// 搜索
const searchQuery = ref('')

// 平台检测
const isMac = computed(() => /Mac/i.test(navigator.platform))

// 响应式
const { isMobile } = useResponsive()
const mobileDrawerVisible = ref(false)
let preloadTimer: ReturnType<typeof setTimeout> | null = null

const warmPrimaryListData = () => Promise.allSettled([
  nodeStore.fetchNodes({ page: 1, page_size: 20 }),
  edgeDeviceStore.fetchList({ page: 1, page_size: 24 }),
])

// 单主体模式下，所有菜单仅由登录状态保护。
const allMenuItems = [
	{ path: '/dashboard', title: '仪表盘', icon: Odometer },
	{ path: '/node', title: '节点', icon: Connection },
	{ path: '/edge-device', title: '边缘设备', icon: Cpu },
	{ path: '/logical-device', title: '逻辑设备', icon: Share },
	{ path: '/data-sources', title: '数据源', icon: Link },
	{ path: '/channel', title: '通道管理', icon: Connection },
	{ path: '/data', title: '数据面板', icon: DataLine },
	{ path: '/firmware', title: '固件管理', icon: Files },
	{ path: '/device-configs', title: '配置模板', icon: Setting },
	{ path: '/monitor', title: '系统监控', icon: DataAnalysis },
	{ path: '/alerts', title: '告警规则', icon: Bell },
	{ path: '/automation', title: '自动化策略', icon: SetUp },
]
const menuItems = computed(() => allMenuItems)

const appVersion = computed(() => import.meta.env.VITE_APP_VERSION || '2.2.0')

// 通知 - 从 API 获取
const notifications = ref<ApiNotification[]>([])
const notificationCount = ref(0)

// 通知列表保留上限：与 REST 默认 limit 一致（api/notification.ts getNotifications(20)），
// 避免实时推送插入导致列表无限增长。
const NOTIFICATION_LIMIT = 20

const fetchNotifications = async () => {
  try {
    const [notifs, count] = await Promise.all([
      getNotifications(NOTIFICATION_LIMIT),
      getUnreadCount()
    ])
    notifications.value = notifs.slice(0, NOTIFICATION_LIMIT)
    notificationCount.value = count
  } catch (error) {
    // API 失败时显示空列表，不报错
    notifications.value = []
    notificationCount.value = 0
  }
}

// insertNotification 实时推送落列表：按 id 去重（重复推送 / 与 REST 拉取重叠）
// + 头部插入 + 未读数 +1（read=false）+ 上限截断。
// 返回 true 表示确实新增了一条（用于测试断言与调用方判断）。
const insertNotification = (raw: Partial<ApiNotification> | undefined | null): boolean => {
  if (!raw || raw.id == null) return false
  const existing = notifications.value.find(item => item.id === raw.id)
  if (existing) {
    // 同 id 已存在：只做就地更新（例如推送与 REST 竞态时字段更全），不重复计数。
    Object.assign(existing, raw)
    return false
  }
  // 后端广播载荷与 models.Notification 同形；这里兜底默认值，避免缺字段导致渲染异常。
  const item: ApiNotification = {
    id: raw.id,
    type: raw.type ?? 'info',
    title: raw.title ?? '',
    description: raw.description ?? '',
    source: raw.source ?? '',
    source_id: raw.source_id,
    read: raw.read ?? false,
    created_at: raw.created_at ?? '',
  }
  notifications.value = [item, ...notifications.value].slice(0, NOTIFICATION_LIMIT)
  if (!item.read) notificationCount.value += 1
  return true
}

// 初始化获取通知
fetchNotifications()

// ── 实时通知：订阅 WS 推送（负债 D-3）──
// 后端 events.Notification 的载荷是通知实体本身（与 REST 列表项同形），收到即插入。
// 订阅 automation 的 3 个自定义事件：它们同样携带通知实体（顶层）与自动化领域详情
// （detail），前端按语义给出差异化用户反馈。
const handleNotificationPush = (message: { payload?: any; data?: any }) => {
  // 兼容两种 WS 消息形状：{type, payload} 与扁平 {type, ...fields}
  const body = (message?.payload ?? message?.data) as Partial<ApiNotification> | undefined
  const inserted = insertNotification(body)
  if (inserted) {
    logger.debug('[MainLayout] 收到实时通知', { id: body?.id })
  }
}

const handleAutomationDailyLimit = (message: { payload?: any; data?: any }) => {
  handleNotificationPush(message)
  const body = (message?.payload ?? message?.data) as any
  const ruleName = body?.detail?.rule_name ?? body?.title ?? ''
  feedback.warning(`策略「${ruleName}」已达日执行上限，今日后续触发将被抑制`)
}

const handleAutomationSystemActorUnavailable = (message: { payload?: any; data?: any }) => {
  handleNotificationPush(message)
  feedback.error('自动化执行已禁用：系统主体用户不可用，请检查初始化状态')
}

const handleAutomationPendingConfirm = (message: { payload?: any; data?: any }) => {
  handleNotificationPush(message)
  const body = (message?.payload ?? message?.data) as any
  const ruleName = body?.detail?.rule_name ?? body?.title ?? ''
  // 待确认事件 → 用户可见提示 + 跳转自动化策略页处理。
  // ElNotification 支持 onClick（ElMessage 不支持），点击提示即前往确认。
  ElNotification({
    type: 'warning',
    duration: 6000,
    title: '策略等待确认',
    message: `策略「${ruleName}」等待人工确认，点击前往处理`,
    onClick: () => { router.push('/automation') },
  })
}

let unsubscribeNotification: (() => void) | null = null
let unsubscribeDailyLimit: (() => void) | null = null
let unsubscribeSystemActor: (() => void) | null = null
let unsubscribePendingConfirm: (() => void) | null = null
// removeConnectionRefresh 解绑 wsStore.onConnected 注册的"重连成功后刷新"处理器。
let removeConnectionRefresh: (() => void) | null = null

// 页面重新可见时刷新（长时间挂后台的页面计数会过期）。
// 刻意不用定时轮询：只在 visibilitychange → visible 这一个真实事件上拉取。
const handleVisibilityChange = () => {
  if (document.visibilityState === 'visible') {
    void fetchNotifications()
  }
}

const setupRealtimeNotifications = () => {
  unsubscribeNotification = wsStore.subscribe(WS_EVENT.NOTIFICATION, handleNotificationPush)
  unsubscribeDailyLimit = wsStore.subscribe('automation_daily_limit', handleAutomationDailyLimit)
  unsubscribeSystemActor = wsStore.subscribe('automation_system_actor_unavailable', handleAutomationSystemActorUnavailable)
  unsubscribePendingConfirm = wsStore.subscribe('automation_pending_confirm', handleAutomationPendingConfirm)
  // WS 重连成功 → 重新拉取，补齐断线期间错过的推送（不依赖轮询）。
  removeConnectionRefresh = wsStore.onConnected(() => { void fetchNotifications() })
  document.addEventListener('visibilitychange', handleVisibilityChange)
}

const teardownRealtimeNotifications = () => {
  unsubscribeNotification?.(); unsubscribeNotification = null
  unsubscribeDailyLimit?.(); unsubscribeDailyLimit = null
  unsubscribeSystemActor?.(); unsubscribeSystemActor = null
  unsubscribePendingConfirm?.(); unsubscribePendingConfirm = null
  removeConnectionRefresh?.(); removeConnectionRefresh = null
  document.removeEventListener('visibilitychange', handleVisibilityChange)
}

// 移动端 logo 点击：关闭抽屉并导航
const handleMobileLogoClick = () => {
  mobileDrawerVisible.value = false
  router.push('/dashboard')
}

// ── 桌面侧栏键盘可达（roving tabindex）──
// Element Plus 的 el-menu 垂直模式没有内置键盘导航：只有在水平模式（mode="horizontal"）
// 才实例化 Menu 类并注册键盘处理，垂直分支零键盘处理。因此这里自行管理 roving tabindex：
// 只有当前项 tabindex=0（可 Tab 进入），其余 -1；方向键在项之间移动焦点。
const desktopMenuRef = ref<any>(null)
const sidebarFocusIndex = ref(0)

// 当前激活项下标 —— 进入侧栏时焦点落在用户当前所在页面，且始终落在有效范围内。
const activeMenuIndex = computed(() => {
  const idx = menuItems.value.findIndex((item) => item.path === activeMenu.value)
  return idx >= 0 ? idx : 0
})

// 共用：从 el-menu 组件实例取真实 DOM 项。
// 用 $el 而不是暴露的方法：expose() 不会移除 $el（Vue 的 publicPropertiesMap 含 $el）。
const menuItemEls = (menuRef: { value: any }): HTMLElement[] => {
  const root = (menuRef.value as any)?.$el as HTMLElement | undefined
  if (!root) return []
  return Array.from(root.querySelectorAll<HTMLElement>('.el-menu-item'))
}

// 共用键盘导航：ArrowUp/Down/Home/End 移动焦点（并同步 roving index），
// Enter/Space 激活当前项。el-menu 渲染为 <li role="menuitem">，原生不响应任何键
// （垂直模式 EP 未注册键盘处理），所以这里必须自己实现。
// 说明：不需要额外的 focus 兜底参数 —— 每个菜单项的 @focus 已把 roving index
// 同步到自身下标（见模板 :tabindex / @focus），键盘与鼠标聚焦都能覆盖。
const makeMenuKeydownHandler = (
  menuRef: { value: any },
  focusIndex: { value: number },
) => {
  const focusItem = (index: number) => {
    const items = menuItemEls(menuRef)
    if (!items.length) return
    const next = Math.min(Math.max(index, 0), items.length - 1)
    focusIndex.value = next
    void nextTick(() => {
      items[next]?.focus()
    })
  }

  return (event: KeyboardEvent) => {
    const target = event.target as HTMLElement | null
    const current = target?.closest?.('.el-menu-item') as HTMLElement | null
    if (!current) return
    const items = menuItemEls(menuRef)
    const currentIndex = Math.max(items.indexOf(current), 0)

    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        focusItem(currentIndex + 1)
        break
      case 'ArrowUp':
        event.preventDefault()
        focusItem(currentIndex - 1)
        break
      case 'Home':
        event.preventDefault()
        focusItem(0)
        break
      case 'End':
        event.preventDefault()
        focusItem(items.length - 1)
        break
      case 'Enter':
      case ' ':
        event.preventDefault()
        current.click()
        break
      default:
        break
    }
  }
}

const handleSidebarKeydown = makeMenuKeydownHandler(desktopMenuRef, sidebarFocusIndex)

// ── 移动端抽屉焦点陷阱 ──
// ElDrawer 自带 ElFocusTrap，但焦点陷阱只有在容器内"存在可聚焦元素"时才生效
// （obtainAllFocusableElements 返回空 → 不 preventDefault → Tab 直接穿到遮罩后的页面）。
// 抽屉内原本零个可聚焦元素，所以这里同样用 roving tabindex 让菜单项可聚焦，
// 陷阱随即接管 Tab 循环；关闭时由 ElFocusTrap 归还焦点（其 lastFocusBeforeTrapped 记录触发按钮）。
const mobileMenuRef = ref<any>(null)
const mobileMenuTriggerRef = ref<any>(null)
const mobileFocusIndex = ref(0)
const mobileDrawerBodyRef = ref<HTMLElement | null>(null)

const mobileMenuItemEls = (): HTMLElement[] => menuItemEls(mobileMenuRef)

const handleMobileMenuKeydown = makeMenuKeydownHandler(mobileMenuRef, mobileFocusIndex)

const mobileDrawerRoot = (): HTMLElement | null =>
  (mobileDrawerBodyRef.value?.closest('.el-drawer') as HTMLElement | null) ?? null

// 兜底：若焦点仍逃逸到抽屉外，立即拉回抽屉内（当前 roving 项）。
// 仅在 ElFocusTrap 失效（容器内无可聚焦元素）时才会真正触发。
const handleMobileDrawerFocusIn = (event: FocusEvent) => {
  const root = mobileDrawerRoot()
  const target = event.target as HTMLElement | null
  if (!root || !target || root.contains(target)) return
  const items = mobileMenuItemEls()
  const fallback = items[mobileFocusIndex.value] ?? items[0] ?? mobileDrawerBodyRef.value
  fallback?.focus()
}

// 打开时：对齐 roving 项并聚焦，随后挂上逃逸兜底；关闭时解绑。
watch(mobileDrawerVisible, (visible) => {
  if (visible) {
    mobileFocusIndex.value = activeMenuIndex.value
    document.addEventListener('focusin', handleMobileDrawerFocusIn)
  } else {
    document.removeEventListener('focusin', handleMobileDrawerFocusIn)
  }
})

const handleMobileDrawerOpened = () => {
  const items = mobileMenuItemEls()
  const target = items[mobileFocusIndex.value] ?? mobileDrawerBodyRef.value
  target?.focus()
}

// 关闭后把焦点归还触发按钮。
// ElFocusTrap 的 lastFocusBeforeTrapped 记录的是"陷阱生效前"的 activeElement，
// 而移动端点开抽屉时焦点往往还在 body 上，于是它归还到 body（键盘用户丢失位置）。
// 这里显式归还到汉堡按钮，保证 Tab 序列从原处继续。仅当焦点未被用户主动移到别处时才抢。
const handleMobileDrawerClosed = () => {
  // afterLeave 里 v-show 会隐藏仍在焦点上的菜单项，浏览器随即把焦点重置到 body；
  // 而 @closed 是在同一次 DOM patch 之前同步派发的。所以要等两帧（DOM 已更新、已绘制）
  // 再判断"焦点是否丢失"，否则会把焦点抢回一个马上被隐藏的元素。
  requestAnimationFrame(() => requestAnimationFrame(() => {
    const trigger = (mobileMenuTriggerRef.value as any)?.$el as HTMLElement | undefined
    if (!trigger) return
    const active = document.activeElement
    const lostFocus = !active || active === document.body || active === mobileDrawerBodyRef.value
    if (lostFocus) trigger.focus()
  }))
}

// 侧边栏宽度
const sidebarWidth = computed(() => {
  return uiStore.sidebarCollapsed ? '64px' : '200px'
})

// 当前激活菜单
const activeMenu = computed(() => {
  return route.path
})

// 路由变化时把桌面侧栏的 roving tabindex 对齐到当前激活项。
// 必须放在 activeMenu 声明之后：watch 创建时会立即读取 source 的 .value，
// 提前声明会命中 TDZ（ReferenceError: Cannot access '$' before initialization）。
watch(activeMenuIndex, (idx) => {
  sidebarFocusIndex.value = idx
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
    // 本地列表与计数同步收敛（不依赖随后的网络往返，避免"点了没反应"的窗口期）。
    notifications.value.forEach(item => { item.read = true })
    notificationCount.value = 0
    await fetchNotifications()
    feedback.success('已全部标记为已读')
  } catch (error) {
    feedback.handleError(error, '标记已读失败')
  }
}

const handleNotificationClick = async (item: ApiNotification) => {
  try {
    await markAsRead(item.id)
    // 已读同步：仅当此前确实未读才递减，避免重复点击把计数减成负数。
    if (!item.read) {
      item.read = true
      notificationCount.value = Math.max(0, notificationCount.value - 1)
    }
  } catch (error) {
    // 静默失败
  }
  // 通知跳转 (方案 v3.3 §六 D-2): 优先按 Notification.source 结构化路由,
  // 替代脆弱的 title 字符串匹配; 离线通知保留原 title 匹配行为。
  //   source=merge_failed       → /logical-device (合并搬迁失败→管理页,
  //     source_id 是 merge_job id, 管理页可按任务查看进度/重试)
  //   source=retention_expiring → /logical-device?retention=<logical_id>
  //     (到期提醒→管理页 retention 编辑深链)
  if (item.source === 'merge_failed') {
    router.push('/logical-device')
    return
  }
  if (item.source === 'retention_expiring') {
    const logicalId = item.source_id
    router.push(logicalId != null && logicalId !== '' ? `/logical-device?retention=${logicalId}` : '/logical-device')
    return
  }
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
  // 首次把 roving tabindex 对齐到当前激活项（此时 activeMenu 已声明，DOM 已挂载）。
  sidebarFocusIndex.value = activeMenuIndex.value
  if (wsStore.isAuthenticated) {
    logger.debug('[MainLayout] 已登录，连接 WebSocket')
    wsStore.connect()
  } else {
    logger.debug('[MainLayout] 未登录，跳过 WebSocket')
  }
  // 实时通知订阅（含 3 个 automation 自定义事件）+ 重连/可见性刷新
  setupRealtimeNotifications()
  document.addEventListener('keydown', handleKeydown)
  preloadTimer = setTimeout(() => {
    void Promise.allSettled([
      preloadPrimaryRoutes(),
      warmPrimaryListData(),
    ])
  }, 0)
})

onUnmounted(() => {
  teardownRealtimeNotifications()
  wsStore.disconnect()
  document.removeEventListener('keydown', handleKeydown)
  document.removeEventListener('focusin', handleMobileDrawerFocusIn)
  if (preloadTimer) clearTimeout(preloadTimer)
})
</script>

<style scoped>
.main-layout {
  height: 100vh;
  overflow: hidden;
}

/* ========== 侧边栏 (桌面端，深色主题) ========== */
.sidebar {
  background: linear-gradient(180deg, #1a1f2e 0%, #1e2538 100%);
  display: flex;
  flex-direction: column;
  transition: width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}

/* 桌面端 logo 区域 — 限定在 .sidebar 内 */
.sidebar .logo-area {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  cursor: pointer;
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
  transition: all 0.3s;
}

.sidebar .logo-area:hover {
  background: rgba(255, 255, 255, 0.04);
}

.sidebar .logo-icon {
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

/* 桌面端 logo 文字 — 白色，仅 .sidebar 内生效 */
.sidebar .logo-text {
  font-size: 18px;
  font-weight: 600;
  color: #fff;
  white-space: nowrap;
}

/* 桌面端菜单 — 限定在 .sidebar 内 */
.sidebar .sidebar-menu {
  flex: 1;
  border-right: none;
  background: transparent;
  padding: 8px;
}

.sidebar :deep(.el-menu-item) {
  height: 44px;
  margin: 2px 0;
  border-radius: 8px;
  color: rgba(255, 255, 255, 0.65);
  transition: all 0.3s;
}

.sidebar :deep(.el-menu-item:hover) {
  background: rgba(255, 255, 255, 0.08);
  color: #fff;
}

/* 键盘焦点可见环 —— 仅在键盘聚焦时出现，不干扰鼠标用户 */
.sidebar :deep(.el-menu-item:focus-visible) {
  outline: 2px solid var(--el-color-primary);
  outline-offset: -2px;
  color: #fff;
}

.sidebar :deep(.el-menu-item.is-active) {
  background: linear-gradient(90deg, rgba(64, 158, 255, 0.2) 0%, rgba(64, 158, 255, 0.1) 100%);
  color: var(--el-color-primary);
}

.sidebar :deep(.el-menu-item.is-active)::before {
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

.sidebar .sidebar-footer {
  padding: 12px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
}

.sidebar .version-info {
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
  background: var(--header-bg);
  border-bottom: 1px solid var(--header-border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
  box-shadow: var(--shadow-sm);
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
  background: var(--hover-bg);
  transform: scale(1.05);
}

/* 移动端页头图标按钮触控目标（规范 §4.4.5 MUST：≥44×44px）。
   Element Plus 的 circle 按钮默认 32×32，这里把实际盒子补到 44×44
   （页头高 60px，放得下，不引起换行）。不使用伪元素外扩热区：
   实测伪元素在部分容器内会被裁剪（el-table 单元格内即失效），
   且不参与布局盒、无法用 getBoundingClientRect 验收，
   真实指针也点不到视觉盒之外的伪元素区域。 */
@media (max-width: 768px) {
  .main-header .el-button.is-circle {
    min-width: 44px;
    min-height: 44px;
  }
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
  background: var(--el-color-danger-light-9);
  border-radius: 20px;
  font-size: 12px;
  color: var(--el-color-danger);
  transition: all 0.3s;
}

.ws-status.connected {
  background: var(--el-color-success-light-9);
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
  border-bottom: 1px solid var(--divider-color);
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
  background: var(--el-color-primary-light-9);
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
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.notification-icon.warning {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-warning);
}

.notification-icon.success {
  background: var(--el-color-success-light-9);
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
  color: var(--text-color-primary);
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
  color: var(--text-color-primary);
}

/* ========== 主内容区 ========== */
.main-content {
  padding: 20px;
  overflow-y: auto;
  height: calc(100vh - 60px);
  background: var(--el-fill-color-light);
  scrollbar-gutter: stable;
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


/* ========== 移动端抽屉 (Teleport 到 body，需用 :global) ========== */
:global(.mobile-sidebar-drawer .el-drawer__body) {
  display: flex;
  flex-direction: column;
  padding: 0;
  background: var(--el-bg-color);
}

:global(.mobile-sidebar-drawer .mobile-drawer-body) {
  display: flex;
  flex-direction: column;
  height: 100%;
}

:global(.mobile-sidebar-drawer .mobile-logo-area) {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--el-border-color-lighter);
  transition: background 0.2s;
}

:global(.mobile-sidebar-drawer .mobile-logo-area:hover) {
  background: var(--el-fill-color-light);
}

:global(.mobile-sidebar-drawer .logo-icon) {
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

:global(.mobile-sidebar-drawer .mobile-logo-text) {
  font-size: 18px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
}

:global(.mobile-sidebar-drawer .mobile-sidebar-menu) {
  flex: 1;
  border-right: none;
  background: var(--el-bg-color);
  padding: 8px;
  overflow-y: auto;
}

:global(.mobile-sidebar-drawer .el-menu-item) {
  height: 44px;
  margin: 2px 0;
  border-radius: 8px;
  color: var(--el-text-color-regular);
  transition: all 0.3s;
}

:global(.mobile-sidebar-drawer .el-menu-item:hover) {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}

:global(.mobile-sidebar-drawer .el-menu-item:focus-visible) {
  outline: 2px solid var(--el-color-primary);
  outline-offset: -2px;
  color: var(--el-color-primary);
}

:global(.mobile-sidebar-drawer .el-menu-item.is-active) {
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}

:global(.mobile-sidebar-drawer .mobile-sidebar-footer) {
  padding: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}

:global(.mobile-sidebar-drawer .mobile-version-info) {
  text-align: center;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
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
  .sidebar .logo-text {
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

<!-- ========== 移动端抽屉 :global 样式 (Teleport 到 body，scoped 无法覆盖) ========== -->
<style>
.mobile-sidebar-drawer {
  /* 抽屉容器背景使用 Element Plus token */
  --el-drawer-bg-color: var(--el-bg-color);
}

.mobile-sidebar-drawer .el-drawer__body {
  background: var(--el-bg-color);
  display: flex;
  flex-direction: column;
  padding: 0;
}

/* 抽屉内 logo 区域 */
.mobile-sidebar-drawer .logo-area {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  cursor: pointer;
  border-bottom: 1px solid var(--el-border-color-light);
  transition: all 0.3s;
  flex-shrink: 0;
}

.mobile-sidebar-drawer .logo-area:hover {
  background: var(--el-fill-color-light);
}

.mobile-sidebar-drawer .logo-text {
  font-size: 18px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
}

/* 抽屉内菜单 */
.mobile-sidebar-drawer .sidebar-menu {
  flex: 1;
  border-right: none;
  background: transparent;
  padding: 8px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
}

.mobile-sidebar-drawer .el-menu-item {
  height: 44px;
  margin: 2px 0;
  border-radius: 8px;
  color: var(--el-text-color-regular);
  transition: all 0.3s;
}

.mobile-sidebar-drawer .el-menu-item:hover {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-primary);
}

.mobile-sidebar-drawer .el-menu-item.is-active {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.mobile-sidebar-drawer .el-menu-item.is-active::before {
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
</style>
