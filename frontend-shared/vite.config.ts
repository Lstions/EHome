import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  // 支持环境变量覆盖 proxy target (docker compose 开发环境用 backend:8080)
  const apiTarget = process.env.VITE_API_TARGET || 'http://localhost:8080'
  const wsTarget = process.env.VITE_API_TARGET
    ? process.env.VITE_API_TARGET.replace('http://', 'ws://')
    : 'ws://localhost:8080'

  return {
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
  server: {
    host: '0.0.0.0',
    strict: false,
    port: 5174,
    proxy: {
      // baseURL 已含 /api/v1, 所以只代理根路径下的后端
      '/api': {
        target: apiTarget,
        changeOrigin: true,
        ws: true,
      },
      '/ws': {
        target: wsTarget,
        ws: true,
      },
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
