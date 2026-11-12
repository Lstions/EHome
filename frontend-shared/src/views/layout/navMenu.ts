/**
 * 侧栏/抽屉导航的**渲染视图**（图标绑定 + 分组视图）。
 *
 * 为什么从 MainLayout.vue 搬出来：这 50 行是「模型 → 视图」的纯映射 ——
 * 路径与标题来自 menuModel.ts 的唯一真值源，本文件只负责**绑定图标**并把
 * 平铺目标集折成分组视图。它不持有任何状态、不碰 DOM、不参与交互，
 * 因此搬走后页面的行为与门禁断言都不受影响（真值源仍是同目录 menuModel.ts）。
 *
 * 边界依据：**渲染**（el-menu / roving tabindex / 键盘处理）留在页面 ——
 * MainLayoutKeyboardNav.spec 的源码契约直接锚在页面文件上（case 'ArrowDown'、
 * item.flatIndex === sidebarFocusIndex），搬走会让门禁失去对象。
 */
import {
  Odometer,
  Connection,
  Cpu,
  Link,
  DataLine,
  Files,
  Setting,
  DataAnalysis,
  SetUp,
  Bell,
  Tickets,
} from '@element-plus/icons-vue'
import { NAV_DESTINATIONS, NAV_GROUPS, assertGroupsCoverDestinations } from './menuModel'

/**
 * 单主体模式下，所有菜单仅由登录状态保护。
 *
 * Phase 0.3：路径与标题来自 `menuModel.ts` 的**唯一真值源**，本文件只负责绑定图标。
 * 这样门禁可按「导航目标集合」判定覆盖（与平铺/分组布局无关），而不是把规模硬编码成
 * 14 —— 后者在 Phase 2 改分组时会整批变红，且无法区分真回归与"布局改了"。
 */
const menuIcons: Record<string, unknown> = {
	'/dashboard': Odometer,
	'/node': Connection,
	'/edge-device': Cpu,
	'/data-sources': Link,
	'/source-groups': Link,
	'/channel': Connection,
	'/data': DataLine,
	'/firmware': Files,
	'/device-configs': Setting,
	'/monitor': DataAnalysis,
	'/alerts': Bell,
	'/automation': SetUp,
	'/notification-channels': Bell,
	'/notification-deliveries': Tickets,
}
// 图标表必须覆盖模型全量：漏配会让菜单项渲染成空白图标（无报错，属静默降级）。
const missingMenuIcon = NAV_DESTINATIONS.find(d => !menuIcons[d.path])
if (import.meta.env.DEV && missingMenuIcon) {
	throw new Error(`[menu] 图标表缺少 ${missingMenuIcon.path} —— 请同步 menuIcons 与 menuModel.NAV_DESTINATIONS`)
}

export interface NavMenuItem {
	path: string
	title: string
	icon: unknown
	/** 扁平下标（**不是**组内下标），roving tabindex 依赖它与 DOM 叶子顺序一致。 */
	flatIndex: number
}

export const allMenuItems: NavMenuItem[] = NAV_DESTINATIONS.map((d, flatIndex) => ({
	path: d.path,
	title: d.title,
	icon: menuIcons[d.path] ?? Tickets,
	flatIndex,
}))

/** 平铺列表（保留：供全局搜索等按顺序消费；menuGroups 是渲染用的分组视图）。 */
export const menuItems: NavMenuItem[] = allMenuItems

// Phase 2.4：分组视图。`flatIndex` 取自 NAV_DESTINATIONS 的下标（**不是**组内下标），
// 这样 roving tabindex 的序号与「分组展开后的 DOM 叶子顺序」一致 ——
// 否则 activeMenuIndex 定位到的下标会指向另一个菜单项，键盘 Tab 进错页。
// 顺序一致性由 menuModel.assertGroupsCoverDestinations() 强制校验。
assertGroupsCoverDestinations()

export const menuGroups = NAV_GROUPS.map(g => ({
	title: g.title,
	items: g.paths.map(p => {
		const item = allMenuItems.find(i => i.path === p)
		// 模型自检已保证 p 必在目标集内，这里的兜底只为类型收窄（不会触发）。
		if (!item) throw new Error(`[menu] 分组引用了未定义的路径 ${p}`)
		return item
	}),
}))

/** el-menu 的 default-openeds：全部组默认展开（见页面模板注释中的键盘可达性理由）。 */
export const allGroupTitles = NAV_GROUPS.map(g => g.title)
