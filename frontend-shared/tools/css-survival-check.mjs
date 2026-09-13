/**
 * 构建产物 CSS 规则存活检查（主控新增，2026-09-13）。
 *
 * 为什么需要它：任务 C 发现 `theme.css` 里新增的 `.el-pagination .el-button { min-width:36px }`
 * 被 Vite 的 CSS 压缩器（esbuild）**误判为 keyframes 名并整条丢弃** ——
 * 源码里规则明明写了、构建成功无报错，但**产物里没有这条规则**，样式不生效。
 *
 * 症状隐蔽：`grep 源码` 能查到、`pnpm build` 成功、但浏览器里没效果。
 * 凡是"断言源码里有这条规则"的测试都抓不到；只有**读构建产物**或**浏览器实测计算样式**才能发现。
 *
 * 用法（先 pnpm build）：
 *   cd frontend-shared && node tools/css-survival-check.mjs
 *
 * 退出码非 0 表示有规则在构建期丢失。
 */
import fs from 'node:fs';
import path from 'node:path';

const DIST = process.env.CSS_DIST || 'dist/assets';
const SRC = process.env.CSS_SRC || 'src/styles/theme.css';

// 需要存活的关键规则：每项是「选择器/属性片段 -> 为什么重要」。
// 只放**有明确理由**的规则，不做全量 diff（全量 diff 会因压缩重排产生大量噪音）。
const REQUIRED = [
  ['pointer:coarse', '规范 §4.4.7 粗指针策略（触控放大）'],
  ['touch-target', '触控热区工具类'],
  ['min-height:44px', '移动端 44px 触控下限（规范 §4.4.5）'],
  ['@keyframes shimmer', '骨架屏动画（theme.css 原有）'],
  ['@keyframes pulse', '脉冲动画'],
  ['@keyframes flash', '闪烁动画'],
  ['.el-drawer', '抽屉兜底（若已加到 theme.css）'],
];

if (!fs.existsSync(DIST)) {
  console.error('找不到 ' + DIST + '，请先在 frontend-shared 下跑 pnpm build');
  process.exit(2);
}

const cssFiles = fs.readdirSync(DIST).filter(f => f.endsWith('.css'));
if (cssFiles.length === 0) {
  console.error(DIST + ' 下没有 .css 产物');
  process.exit(2);
}

// 主入口 CSS 通常最大；把所有 CSS 拼起来判断更稳（规则可能被分到异步 chunk）。
let all = '';
for (const f of cssFiles) all += fs.readFileSync(path.join(DIST, f), 'utf8');

console.log('检查 ' + cssFiles.length + ' 个 CSS 产物，共 ' + all.length + ' 字节\n');

let missing = [];
for (const [needle, why] of REQUIRED) {
  // 压缩后可能去掉空格，做成"去空白包含"判断
  const compactAll = all.replace(/\s+/g, '');
  const compactNeedle = needle.replace(/\s+/g, '');
  const found = all.includes(needle) || compactAll.includes(compactNeedle);
  const srcHas = fs.existsSync(SRC) ? fs.readFileSync(SRC, 'utf8').includes(needle) : false;
  if (found) {
    console.log('  ✅ ' + needle);
  } else if (srcHas) {
    console.log('  ❌ ' + needle + ' —— **源码有、产物没有**（构建期丢失！）  ' + why);
    missing.push(needle);
  } else {
    console.log('  ⏭  ' + needle + ' —— 源码也没有，跳过（' + why + '）');
  }
}

if (missing.length > 0) {
  console.error('\n有 ' + missing.length + ' 条规则在构建期丢失：' + missing.join(', '));
  console.error('排查方向：CSS 压缩器把规则误判为 @keyframes、语法被静默忽略等。');
  process.exit(1);
}
console.log('\n全部关键规则在产物中存活。');
