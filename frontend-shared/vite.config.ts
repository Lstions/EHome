import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { execSync } from 'node:child_process'
import { fileURLToPath, URL } from 'node:url'

/**
 * 构建身份（契约 §4.3 / T2.4）：`build_id` 是「旧页面是否过期」的唯一判据，
 * 必须与后端 `buildinfo.BuildID` **同源**，且**长度必须一致（7 位）** ——
 * 后端 BuildID 取 git short SHA 前 7 位（Dockerfile 用 `GIT_COMMIT` 前 7 位推导，
 * 与本文件规则一致）。传 12 位不会被纠正，只会让前端永远对不上
 * `X-EHome-Build` 响应头 ⇒ 页面常驻「系统已发布新版本」横幅。
 * **刻意不做截断/补全**：静默修正会掩盖 CI 传错，问题会以更难排查的方式出现。
 *
 * 取值优先级：
 *   1. 环境变量 `VITE_BUILD_ID`（CI / Docker build-arg 注入，**生产推荐**，
 *      与后端 buildinfo 用同一个值）；
 *   2. 仓库 git 短 SHA（本地 `vite build` 时自动得到，同 commit 重编幂等）；
 *   3. `dev-<时间戳36进制>`（无 git 的构建环境，保证每次构建可区分）。
 *
 * `build_time` 用于「部署回滚不提示」判定：服务端时间比本地更旧即视为回滚。
 */
function resolveBuildId(env: Record<string, string>): string {
  const fromEnv = (env.VITE_BUILD_ID || '').trim()
  if (fromEnv) return fromEnv
  try {
    // ⚠️ **必须 7 位**：后端 buildinfo.BuildID 取短 SHA 前 7 位，这里也取 7 位。
    //    取 12 位会让本地构建产物永远对不上 `X-EHome-Build` ⇒ 常驻「有新版本」横幅。
    //    仍然不做截断/补全：显式传入的 VITE_BUILD_ID 原样使用，传错要在构建/部署期暴露。
    const sha = execSync('git rev-parse --short=7 HEAD', {
      stdio: ['ignore', 'pipe', 'ignore'],
    })
      .toString()
      .trim()
    if (sha) return sha
  } catch {
    /* 无 git（tarball / 构建沙箱）：走时间戳兜底 */
  }
  return `dev-${Date.now().toString(36)}`
}

function resolveBuildTime(env: Record<string, string>): string {
  const fromEnv = (env.VITE_BUILD_TIME || '').trim()
  return fromEnv || new Date().toISOString()
}

// https://vitejs.dev/config/
export default defineConfig(({ command, mode }) => {
  // 统一从 .env* 文件读取环境变量（与前端 import.meta.env 行为一致）。
  // 注意：不能用 process.env 读 VITE_* —— vite 不会把 .env 文件注入 process.env，
  // 用 process.env 会导致 vite.config 与前端 import.meta.env 读到不一致的值。
  const env = loadEnv(mode, process.cwd(), '')
  // 默认代理到 :8080 的 ehome-web 容器（前端 SPA + 后端 API 同容器，且是唯一
  // 连着 EMQX、真正在收设备数据的实例）。
  // ⚠️ 曾经默认 :8082，但该端口已被**无关项目** digital-family-tree 的容器占用：
  // 代理指过去时端口确实有服务在监听，因此不会报错，只会 REST 404 + WebSocket
  // 永久握手失败 ⇒ 界面「一直显示离线」。默认值必须指向真正的 EHome 后端。
  const apiTarget = env.VITE_API_TARGET || 'http://localhost:8080'
  const wsTarget = env.VITE_API_TARGET
    ? env.VITE_API_TARGET.replace('http://', 'ws://')
    : 'ws://localhost:8080'

  // 子路径前缀部署（反代 /ehome-dev 场景）：VITE_BASE_PATH=/ehome-dev/ 时启用
  const basePath = env.VITE_BASE_PATH || '/'
  // 去掉首尾斜杠，用于 proxy key 拼接（如 /ehome-dev/api）
  const basePrefix = basePath === '/' ? '' : basePath.replace(/\/+$/, '')

  const buildId = resolveBuildId(env)
  const buildTime = resolveBuildTime(env)

  return {
  // 构建身份只注入**生产构建**：`vite dev` 下前端热更新、后端不是同一份产物，
  // 两者的 build_id 天然不同源 ⇒ 注入一个「本地 id」会让开发时永远挂着
  // 「系统已发布新版本」横幅。开发态留空 = 更新检测静默（store 视为「无法比对」）。
  // 需要在本机验证横幅时，显式 `VITE_BUILD_ID=xxx pnpm build && pnpm preview`。
  define: command === 'build'
    ? {
        'import.meta.env.VITE_BUILD_ID': JSON.stringify(buildId),
        'import.meta.env.VITE_BUILD_TIME': JSON.stringify(buildTime),
      }
    : {},
  plugins: [
    vue(),
    // Element Plus 按需自动引入
    AutoImport({
      imports: ['vue', 'vue-router', 'pinia'],
      resolvers: [ElementPlusResolver()],
      dts: 'src/auto-imports.d.ts',
    }),
    Components({
      resolvers: [
        // 自动按需引入 Element Plus 组件和样式
        ElementPlusResolver({ importStyle: 'css', dts: 'src/components.d.ts' }),
      ],
      // 自定义组件位置（项目内）
      dirs: ['src/components'],
      dts: 'src/components.d.ts',
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  // 子路径前缀部署（反代 /ehome-dev 场景）
  base: basePath,
  server: {
    host: '0.0.0.0',
    // 放行任意 Host 访问 dev server（自定义域名/反代入口，如 console.qsun.fun）
    allowedHosts: true,
    port: 5174,
    proxy: {
      // baseURL 已含 /api/v1, 所以只代理根路径下的后端
      //
      // ⚠️ `changeOrigin` 必须为 **false**（此处显式写出，防止被"顺手改成 true"）：
      // 后端 WebSocket 升级有 CSRF 防护 —— `checkOrigin` 要求
      // `Origin.host === r.Host`（backend/internal/websocket/websocket.go:82），
      // 否则 403 "request origin not allowed by Upgrader.CheckOrigin"。
      //
      // changeOrigin:true 会把 Host 改写成后端地址（127.0.0.1:8080），
      // 而浏览器发的 Origin 仍是 dev server 地址（127.0.0.1:5174）⇒ 两者不等 ⇒
      // **WS 必然 403**，前端 NetworkBanner 持续显示
      // 「与服务器的连接已断开／正在尝试重新连接...」并每 10s 重连。
      // 保持 false 则 Host 原样透传，与 Origin 一致 ⇒ 101 Switching Protocols。
      //
      // 为什么不改后端放行：生产部署前端 dist 与后端**同容器同端口**
      // （EHOME_EXTERNAL_HOST=<host>:8080），同源校验在生产完全正确，属安全设计
      // （提交 6b764ffa 加固）。只有 dev 的"5174 前端 + 8080 后端"跨源场景需要对齐，
      // 故在代理侧解决。
      //
      // 验证提示：`curl` 不发 Origin 头时后端直接放行（`origin == ""` → true），
      // 因此用 curl 测 WS 会**假绿** —— 必须用真实浏览器或显式带 Origin 才测得准。
      '/api': {
        target: apiTarget,
        changeOrigin: false,
        ws: true,
      },
      '/ws': {
        target: wsTarget,
        changeOrigin: false,
        ws: true,
      },
      // 反代前缀场景：/ehome-dev/api -> 后端 /api（剥离前缀）
      ...(basePrefix ? {
        [`${basePrefix}/api`]: {
          target: apiTarget,
          changeOrigin: false,
          ws: true,
          rewrite: (path: string) => path.replace(new RegExp(`^${basePrefix}`), ''),
        },
        [`${basePrefix}/ws`]: {
          target: wsTarget,
          changeOrigin: false,
          ws: true,
          rewrite: (path: string) => path.replace(new RegExp(`^${basePrefix}`), ''),
        },
      } : {}),
    },
  },
  build: {
    // 提升主 chunk 体积阈值到 1.5MB（ECharts 部分按需引入后会远低于此）
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        // ECharts / xterm / 业务通用 chunk 拆分
        manualChunks(id) {
          if (id.includes('node_modules/echarts/')) return 'echarts'
          if (id.includes('node_modules/element-plus/')) return 'element'
          if (id.includes('node_modules/vue/') || id.includes('node_modules/vue-router/') || id.includes('node_modules/pinia/')) return 'vue'
        },
      },
    },
  },
  }
})
