/**
 * 子路径前缀部署（反代 /ehome-dev 场景）工具。
 *
 * VITE_BASE_PATH 由 vite 注入（vite.config.ts 的 base 配置），
 * 值为 `/ehome-dev/`（带首尾斜杠）或 `/`（默认无前缀）。
 * 本工具提供统一的路径拼接函数，供硬编码绝对路径跳转使用
 * （如 window.location.assign('/login') 需要带前缀）。
 */

/** 去掉尾部斜杠的前缀，如 `/ehome-dev/` -> `/ehome-dev`；无前缀时返回空串 */
export function basePrefix(): string {
  const base = import.meta.env.VITE_BASE_PATH as string | undefined
  if (!base || base === '/' || base === '') return ''
  return base.replace(/\/+$/, '')
}

/**
 * 把应用内绝对路径（如 `/login`）拼接上前缀，得到可访问的完整路径
 * （如 `/ehome-dev/login`）。无前缀时原样返回。
 */
export function withBase(path: string): string {
  const prefix = basePrefix()
  return prefix ? `${prefix}${path}` : path
}

/** 当前登录页完整路径（带前缀） */
export function loginPath(): string {
  return withBase('/login')
}
