import type { Directive, DirectiveBinding } from 'vue'
import { watch } from 'vue'
import { useUserStore } from '@/stores/user'

/**
 * v-permission 权限指令
 *
 * 用法:
 *   v-permission="'admin'"                    // 单角色
 *   v-permission="['admin', 'operator']"     // 多角色（任一匹配即可）
 *   v-permission:any="['admin', 'operator']" // 修饰符 any 同上
 *   v-permission:all="['admin', 'operator']" // 修饰符 all 表示必须全部匹配
 *
 * 无权限时默认从 DOM 移除元素。
 */
interface PermissionElement extends HTMLElement {
  __permission_original_display__?: string
  __permission_watch_stop__?: () => void
}

function checkPermission(value: any, modifier: string): boolean {
  if (value === undefined || value === null || value === '') return true
  const userStore = useUserStore()
  const role = userStore.userInfo?.role
  if (!role) return false

  const allowed = Array.isArray(value) ? value : [value]
  if (modifier === 'all') {
    return allowed.every((r) => r === role)
  }
  // 默认 any 修饰符
  return allowed.includes(role)
}

function applyPermission(el: PermissionElement, binding: DirectiveBinding) {
  const { value, arg, modifiers } = binding
  // 取修饰符：v-permission:any/all 同时支持简写 v-permission="..." 默认 any
  const modifier: string = arg || (modifiers.all ? 'all' : 'any')
  if (checkPermission(value, modifier)) {
    // 恢复显示
    if (el.style.display === 'none' && el.__permission_original_display__ !== undefined) {
      el.style.display = el.__permission_original_display__
    }
  } else {
    // 隐藏
    if (el.__permission_original_display__ === undefined) {
      el.__permission_original_display__ = el.style.display
    }
    el.style.display = 'none'
  }
}

const permission: Directive<PermissionElement, any> = {
  mounted(el, binding: DirectiveBinding) {
    applyPermission(el, binding)
    // 监听用户角色变化，自动重新评估权限
    const userStore = useUserStore()
    el.__permission_watch_stop__ = watch(
      () => userStore.userInfo?.role,
      () => applyPermission(el, binding)
    )
  },
  updated(el, binding: DirectiveBinding) {
    applyPermission(el, binding)
  },
  unmounted(el) {
    // 清理 watcher
    el.__permission_watch_stop__?.()
  },
}

export default permission
