import { defineStore } from 'pinia'

/**
 * 顶部框架面包屑的**末段文案覆盖**（2026-10-21 用户要求）。
 *
 * 用户原话：「顶部面包屑不应该显示『边缘设备详情』，应该显示边缘设备名称」。
 *
 * 为什么需要 store 而不是路由 meta：
 *   设备名称是**异步**取到的（详情接口返回后才知道），而路由 meta 在导航时就固定了。
 *   同时末段必须跟着「当前正在看哪台设备」变（同路由不同 id 直接切走时也要更新）。
 *
 * 契约：
 *   · 详情页在拿到名称后 setLeafLabel(name)，组件卸载时 clearLeafLabel()；
 *   · MainLayout 只把 label 用在**最后一段**，中间段（可点回列表）仍用模块名；
 *   · 空字符串 = 不覆盖，回退到按路由段推导的默认文案。
 */
export const useBreadcrumbStore = defineStore('breadcrumb', {
  state: () => ({
    /** 末段覆盖文案；'' 表示不覆盖（用路由推导的默认值） */
    leafLabel: '',
  }),

  actions: {
    setLeafLabel(label: string) {
      this.leafLabel = (label || '').trim()
    },
    clearLeafLabel() {
      this.leafLabel = ''
    },
  },
})
