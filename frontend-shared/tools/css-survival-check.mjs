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
  // ── 遮罩 / 毛玻璃（2026-09-29 新增）──────────────────────────────
  // 为什么把这一族纳入构建产物门禁（而不是只写 vitest 源码断言）：
  // 「页面切换黑遮罩」这个 bug 在生产上可见、在 dev 下**完全不可见**（dev 下 EP 后加载
  // 把遮罩变量覆盖成白色）。任何"断言源码里有这条规则"的测试都抓不到这类问题 ——
  // 只有读构建产物、或浏览器实测计算样式才能发现。故这里同时锁「规则在产物里」
  // 与「变量的解析结果不再是纯黑」两件事。
  ['backdrop-filter', '遮罩毛玻璃（v-loading / 对话框）；缺失会退回纯色遮罩'],
  ['--mask-bg', '遮罩底色 token；缺失会让 --el-mask-color 解析失败退回纯黑'],
  ['--mask-blur', '模糊半径 token；缺失则 blur() 无值'],
  ['--overlay-bg', '对话框遮罩底色 token'],
];

// ── 产物级「解析结果」断言（比字符串存在更强的判据）────────────────────
// 说明：CSS 变量会被 var() 逐层解析，字符串搜 'rgba(0,0,0,0.7)' 既可能误报也可能漏报。
// 这里只做**关键否定断言**：项目产物中不得再出现「亮色下把遮罩定成 70% 黑」的那条历史缺陷值。
// 允许出现在 EP 自己的产物（element-*.css）里 —— 那是上游默认值，项目不消费它。
//
// ⚠️ 为什么必须同时匹配 hex 与 rgba 两种写法（这是本门禁第一次写错的地方）：
// esbuild 会把 `rgba(0, 0, 0, 0.7)` **压缩成 `#000000b3`**（alpha 折进 8 位 hex）。
// 只写 /rgba\(0,\s*0,\s*0,\s*0?\.7\)/ 的话，源码变异后门禁**依然报绿** ——
// 属于典型的「门禁看起来在工作、实际抓不到」的假门禁。两种形态都锁。
const FORBIDDEN_IN_PROJECT_CSS = [
  [
    /--el-mask-color:\s*(?:rgba\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0?\.7\s*\)|#000000b3|#000c)/,
    '回归到「亮色主题 70%~80% 黑遮罩」的原始缺陷值（rgba(0,0,0,0.7) / 压缩后的 #000000b3）',
  ],
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
// 项目自己的 CSS = 非 element-*.css 的那些（theme.css 打包在其中）。
let projectCss = '';
for (const f of cssFiles) {
  const txt = fs.readFileSync(path.join(DIST, f), 'utf8');
  all += txt;
  if (!/^element-.*\.css$/.test(f)) projectCss += txt;
}

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

// 否定断言（只在项目自己的 CSS 上判，避免误伤 EP 上游默认值）
for (const [re, why] of FORBIDDEN_IN_PROJECT_CSS) {
  const compact = projectCss.replace(/\s+/g, '');
  if (re.test(projectCss) || re.test(compact)) {
    console.log('  ❌ 项目产物中出现被禁值：' + why);
    missing.push(String(re));
  } else {
    console.log('  ✅ 未出现被禁值（' + why + '）');
  }
}

if (missing.length > 0) {
  console.error('\n有 ' + missing.length + ' 条规则在构建期丢失：' + missing.join(', '));
  console.error('排查方向：CSS 压缩器把规则误判为 @keyframes、语法被静默忽略等。');
  process.exit(1);
}
console.log('\n全部关键规则在产物中存活。');

