/**
 * 亮/暗双主题 WCAG 对比度实测（可访问性 F4 验收探针）
 *
 * 用法：
 *   node tools/theme-contrast-measure.mjs --base=http://127.0.0.1:8082 --label=after
 *   node tools/theme-contrast-measure.mjs --base=http://127.0.0.1:8082 --label=before --variant=before
 *
 * 产出：
 *   /tmp/theme-contrast/<label>.json  逐元素实测（前景 / 有效背景 / 字号 / 阈值 / 判定）
 *   stdout                            表格 + FAIL 清单
 *
 * 方法（沿用 D1 域 uiux-d1-contrast.mjs 的 WCAG 实现，并补掉它记录过的两个坑）：
 *   1. 前景色取 getComputedStyle(el).color；带 alpha 时按有效背景做 alpha 合成；
 *   2. 有效背景沿祖先链收集**所有**非透明 background-color 后自底向上合成
 *      （只取"第一个非透明背景"会在半透明叠加层上算错）；
 *      祖先若靠 background-image(渐变) 承载底色（侧栏），取渐变第一个颜色停止点作底；
 *   3. 阈值：正文 4.5；大字（>=18.66px，或 >=14px 且 >=700）3.0；
 *      placeholder / 禁用态 / 版本号等修饰性元素按 3.0 判定（主控裁决：不得低于 3:1）；
 *   4. 只读 getComputedStyle，不改 DOM，可对同一实例重复跑。
 *
 * `--variant=before`：在页面里追加一段样式，把 CSS 变量值还原成 git HEAD 的取值
 * （改前 theme.css 没有 EP 桥接，EP 变量即 Element Plus 默认值）。
 * 这样前后对照使用同一份 DOM/选择器/算法，只差变量值，避免"跨构建对比"引入噪声。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';

const arg = (k, d) => (process.argv.find((a) => a.startsWith('--' + k + '=')) || '').split('=').slice(1).join('=') || d;
const BASE = arg('base', 'http://127.0.0.1:8082');
const LABEL = arg('label', 'run');
const VARIANT = arg('variant', 'after');
const OUT_DIR = '/tmp/theme-contrast';
const CHROME = process.env.CHROME_PATH || '/snap/bin/chromium';
const USER = arg('user', 'admin');
const PASS = arg('pass', 'UiuxAudit2026!');

const PROBES = [
  [
    "el-tag--success",
    "/automation",
    ".el-tag--success",
    "EP 浅底 success 标签（已执行）",
    "normal"
  ],
  [
    "el-tag--warning",
    "/automation",
    ".el-tag--warning",
    "EP 浅底 warning 标签（设备动作）",
    "normal"
  ],
  [
    "el-tag--danger",
    "/automation",
    ".el-tag--danger",
    "EP 浅底 danger 标签（已过期）",
    "normal"
  ],
  [
    "el-tag-primary",
    "/profile",
    ".el-tag--primary",
    "EP 浅底 primary 徽标（系统管理员）",
    "normal"
  ],
  [
    "el-tag-info",
    "/edge-device/7053",
    ".el-tag--info.is-light",
    "EP 浅底 info 标签",
    "normal"
  ],
  [
    "el-alert--warning",
    "/monitor",
    ".el-alert--warning .el-alert__title",
    "EP 浅黄警告条文字",
    "normal"
  ],
  [
    "el-button--primary",
    "/automation",
    ".el-button--primary",
    "主按钮白字（实心 primary）",
    "normal"
  ],
  [
    "el-table-th",
    "/automation",
    ".el-table th .cell",
    "EP 表头文字",
    "normal"
  ],
  [
    "stat-label",
    "/monitor",
    ".stat-label",
    "统计卡标签",
    "normal"
  ],
  [
    "page-header-subtitle",
    "/channel",
    ".page-header-subtitle",
    "页头副标题",
    "normal"
  ],
  [
    "el-table__empty-text",
    "/logical-device",
    ".el-table__empty-text",
    "EP 表格空态文案",
    "normal"
  ],
  [
    "el-empty__description",
    "/logical-device",
    ".el-empty__description",
    "EP 空态描述",
    "normal"
  ],
  [
    "ws-status",
    "/dashboard",
    ".ws-status .status-text",
    "顶栏 WS 在线徽标（自研浅底）",
    "normal"
  ],
  [
    "status-tag",
    "/node",
    ".status-tag",
    "节点列表离线徽标",
    "normal"
  ],
  [
    "status-indicator",
    "/edge-device",
    ".status-indicator",
    "边缘设备状态徽标",
    "normal"
  ],
  [
    "badge",
    "/node/1",
    ".badge",
    "节点详情状态徽标（.badge-gray）",
    "normal"
  ],
  [
    "metrics-offline-tag",
    "/node/1",
    ".metrics-offline-tag",
    "指标卡离线小标",
    "normal"
  ],
  [
    "device-link",
    "/dashboard",
    ".device-link",
    "表格内设备链接",
    "normal"
  ],
  [
    "alert-value",
    "/dashboard",
    ".alert-value",
    "异常摘要卡数值",
    "normal"
  ],
  [
    "control-metric-big",
    "/monitor",
    ".control-metric strong",
    "监控大号数值（大字 22px/700）",
    "large"
  ],
  [
    "radio-button-active",
    "/dashboard",
    ".el-radio-button.is-active .el-radio-button__inner",
    "时间范围选中项白字",
    "normal"
  ],
  [
    "el-link--primary",
    "/login",
    ".el-link.el-link--primary",
    "忘记密码链接",
    "normal"
  ],
  [
    "login-brand-desc",
    "/login",
    ".brand-desc",
    "登录页品牌描述",
    "normal"
  ],
  [
    "login-version",
    "/login",
    ".version",
    "登录页版本号（修饰性）",
    "decorative"
  ],
  [
    "input-placeholder",
    "/login",
    ".el-input__inner",
    "输入框 placeholder（修饰性）",
    "placeholder"
  ],
  [
    "error-desc",
    "/no-such-route",
    ".error-desc",
    "404 页描述文字",
    "normal"
  ],
  [
    "sidebar-version",
    "/dashboard",
    ".version-info",
    "侧栏版本号（修饰性）",
    "decorative"
  ],
  [
    "sidebar-menu-item",
    "/dashboard",
    ".sidebar .el-menu-item",
    "侧栏菜单项",
    "normal"
  ],
  [
    "el-breadcrumb",
    "/node",
    ".el-breadcrumb__inner",
    "面包屑",
    "normal"
  ],
  [
    "text-muted",
    "/channel",
    ".text-muted",
    "次级灰文字工具类",
    "normal"
  ],
  // 下拉菜单里的禁用项（审计实测 1.75，最差项）；点击触发后测量
  ["dropdown-disabled", "/dashboard", ".el-dropdown-menu__item.is-disabled", "下拉菜单禁用项（主题当前模式）", "decorative"]
];

function measure(sel, mode) {
  var el = document.querySelector(sel);
  if (!el) return { found: false };
  var r = el.getBoundingClientRect();
  if (r.width <= 0 || r.height <= 0) return { found: false, hidden: true };
  var parse = function (c) {
    c = String(c);
    // color(srgb r g b / a)：Chromium 对 color-mix()/相对色解析后的常见输出（分量是 0-1）
    var srgb = c.match(/color\(srgb\s+([^)]+)\)/);
    if (srgb) {
      var q = srgb[1].split(/[\s/]+/).filter(Boolean).map(Number);
      var a = q.length > 3 ? q[3] : 1;
      return [q[0] * 255, q[1] * 255, q[2] * 255, a];
    }
    var m = c.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    var p = m[1].split(/[,\s/]+/).filter(Boolean).map(Number);
    return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1];
  };
  var hex = function (h) { var n = parseInt(h.slice(1), 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1]; };
  var over = function (fg, bg) { return [0, 1, 2].map(function (i) { return fg[i] * fg[3] + bg[i] * (1 - fg[3]); }).concat(1); };
  // 渐变里第一个颜色停止点：用于侧栏这类 background-image 承载的底色
  var firstStop = function (img) {
    if (!img || img === "none") return null;
    var m = img.match(/rgba?\([^)]+\)|#[0-9a-fA-F]{3,8}/g);
    if (!m) return null;
    var c = m[0].charAt(0) === "#" ? hex(m[0]) : parse(m[0]);
    return c && c[3] >= 1 ? c : null;
  };
  var cs = getComputedStyle(el);
  var e = el, stack = [], note = "";
  while (e) {
    var s = getComputedStyle(e);
    var bg = parse(s.backgroundColor);
    if (bg && bg[3] > 0) stack.push(bg);
    var stop = firstStop(s.backgroundImage);
    if ((!bg || bg[3] === 0) && stop) { stack.push(stop); note = "gradient-base"; break; }
    if (bg && bg[3] === 1) break;
    e = e.parentElement;
  }
  var base = [255, 255, 255, 1];
  for (var i = stack.length - 1; i >= 0; i--) base = over(stack[i], base);
  var fg = parse(cs.color) || [0, 0, 0, 1];
  if (fg[3] < 1) fg = over(fg, base);
  if (mode === "placeholder") {
    var ph = parse(getComputedStyle(el, "::placeholder").color);
    if (ph) { fg = ph[3] < 1 ? over(ph, base) : ph; }
  }
  return {
    found: true, note: note,
    fg: "rgb(" + fg.slice(0, 3).map(Math.round).join(", ") + ")",
    bg: "rgb(" + base.slice(0, 3).map(Math.round).join(", ") + ")",
    fontSize: parseFloat(cs.fontSize),
    fontWeight: Number(cs.fontWeight) || 400,
    text: (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 24),
  };
}

const BEFORE_LIGHT = ":root{\n  --color-primary:#409eff; --color-primary-light:#79bbff; --color-primary-lighter:#a0cfff;\n  --color-success:#67c23a; --color-warning:#e6a23c; --color-danger:#f56c6c; --color-info:#909399;\n  --text-color-regular:#606266; --text-color-secondary:#909399; --text-color-placeholder:#a8abb2;\n  /* 改前 theme.css 没有 EP 桥接，以下全部回落 Element Plus 默认值 */\n  --el-color-primary:#409eff; --el-color-primary-rgb:64, 158, 255;\n  --el-color-primary-light-3:#79bbff; --el-color-primary-light-5:#a0cfff; --el-color-primary-light-7:#c6e2ff;\n  --el-color-primary-light-8:#d9ecff; --el-color-primary-light-9:#ecf5ff;\n  --el-color-success:#67c23a; --el-color-success-rgb:103, 194, 58;\n  --el-color-success-light-3:#95d475; --el-color-success-light-5:#b3e19d; --el-color-success-light-7:#d1edc4;\n  --el-color-success-light-8:#e1f3d8; --el-color-success-light-9:#f0f9eb;\n  --el-color-warning:#e6a23c; --el-color-warning-rgb:230, 162, 60;\n  --el-color-warning-light-3:#eebe77; --el-color-warning-light-5:#f3d19e; --el-color-warning-light-7:#f8e3c5;\n  --el-color-warning-light-8:#faecd8; --el-color-warning-light-9:#fdf6ec;\n  --el-color-danger:#f56c6c; --el-color-danger-rgb:245, 108, 108;\n  --el-color-danger-light-3:#f89898; --el-color-danger-light-5:#fab6b6; --el-color-danger-light-7:#fcd3d3;\n  --el-color-danger-light-8:#fde2e2; --el-color-danger-light-9:#fef0f0;\n  --el-color-info:#909399; --el-color-info-rgb:144, 147, 153;\n  --el-color-info-light-3:#b1b3b8; --el-color-info-light-5:#c8c9cc; --el-color-info-light-7:#dedfe0;\n  --el-color-info-light-8:#e9e9eb; --el-color-info-light-9:#f4f4f5;\n  --el-text-color-regular:#606266; --el-text-color-secondary:#909399;\n  --el-text-color-placeholder:#a8abb2; --el-text-color-disabled:#c0c4cc;\n  --el-fill-color:#f0f2f5; --el-fill-color-light:#f5f7fa; --el-fill-color-lighter:#fafafa; --el-fill-color-blank:#fff;\n}";
const BEFORE_DARK = "html.dark{\n  --text-color-regular:#a3a6ad; --text-color-secondary:#8d9095; --text-color-placeholder:#6c7080;\n  --el-text-color-regular:#a3a6ad; --el-text-color-secondary:#8d9095;\n  --el-text-color-placeholder:#6c7080; --el-text-color-disabled:#6c6e72;\n  --el-color-primary:#409eff; --el-color-success:#67c23a; --el-color-warning:#e6a23c;\n  --el-color-danger:#f56c6c; --el-color-error:#f56c6c; --el-color-info:#909399;\n  --el-color-primary-light-3:#3375b9; --el-color-primary-light-5:#2a598a; --el-color-primary-light-7:#213d5b;\n  --el-color-primary-light-8:#1d3043; --el-color-primary-light-9:#18222b;\n  --el-color-success-light-3:#4e8e2f; --el-color-success-light-5:#3e6b27; --el-color-success-light-7:#2d481f;\n  --el-color-success-light-8:#25371c; --el-color-success-light-9:#1c2518;\n  --el-color-warning-light-3:#a77730; --el-color-warning-light-5:#7d5b28; --el-color-warning-light-7:#533f20;\n  --el-color-warning-light-8:#3e301c; --el-color-warning-light-9:#292218;\n  --el-color-danger-light-3:#b25252; --el-color-danger-light-5:#854040; --el-color-danger-light-7:#582e2e;\n  --el-color-danger-light-8:#412626; --el-color-danger-light-9:#2a1d1d;\n  --el-color-info-light-3:#6b6d71; --el-color-info-light-5:#525457; --el-color-info-light-7:#393a3c;\n  --el-color-info-light-8:#2d2d2f; --el-color-info-light-9:#202121;\n}";

const luminance = (rgb) => {
  const m = rgb.match(/[\d.]+/g).map(Number).slice(0, 3);
  const s = m.map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); });
  return 0.2126 * s[0] + 0.7152 * s[1] + 0.0722 * s[2];
};
const ratio = (a, b) => { const l1 = luminance(a), l2 = luminance(b); return +(((Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05))).toFixed(2); };
const threshold = (m, tier) => {
  if (tier === 'decorative' || tier === 'placeholder') return 3;
  const large = m.fontSize >= 18.66 || (m.fontSize >= 14 && m.fontWeight >= 700);
  return large ? 3 : 4.5;
};

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const results = {};
const stamp = new Date().toISOString();

async function login(page) {
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await page.waitForTimeout(1200);
}

for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1, colorScheme: theme });
  await ctx.addInitScript((t) => { try { localStorage.setItem('theme', t); } catch (e) {} }, theme);
  await ctx.addInitScript('window.measure = ' + measure.toString() + ';');
  const page = await ctx.newPage();
  results[theme] = {};

  // 每次导航后：① 关掉过渡/动画（否则会取到 color 过渡的插值中间值，前几次实测已踩坑）；
  //             ② variant=before 时把变量值还原为改前取值（addStyleTag 文档序晚于应用样式表，同特异度即胜出）。
  const inject = async () => {
    await page.addStyleTag({ content: '*{transition:none !important;animation:none !important}' });
    if (VARIANT === 'before') await page.addStyleTag({ content: theme === 'dark' ? BEFORE_DARK : BEFORE_LIGHT });
    await page.waitForTimeout(120);
  };

  // /login（未登录）
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  await inject();
  for (const [key, path, sel, desc, tier] of PROBES.filter((p) => p[1] === '/login')) {
    const raw = await page.evaluate(([s, mode]) => measure(s, mode), [sel, tier === 'placeholder' ? 'placeholder' : 'normal']);
    results[theme][key] = { page: path, sel, desc, tier, ...raw };
  }

  await login(page);
  const byPage = {};
  for (const p of PROBES.filter((x) => x[1] !== '/login')) (byPage[p[1]] ||= []).push(p);
  for (const [path, probes] of Object.entries(byPage)) {
    await page.goto(BASE + path, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(2200);
    await inject();
    for (const [key, , sel, desc, tier] of probes) {
      const raw = await page.evaluate(([s, mode]) => measure(s, mode), [sel, 'normal']);
      results[theme][key] = { page: path, sel, desc, tier, ...raw };
    }
  }

  // 下拉菜单禁用项：主题下拉里的"当前模式"项（审计实测 1.75，全站最差）
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  await inject();
  await page.click('button[aria-label="切换主题"]').catch(() => {});
  await page.waitForTimeout(900);
  await inject();
  const rawMenu = await page.evaluate(([s]) => measure(s, 'normal'), ['.el-dropdown-menu__item.is-disabled']);
  results[theme]['dropdown-disabled'] = { page: '/dashboard(主题下拉)', sel: '.el-dropdown-menu__item.is-disabled', desc: '主题菜单禁用项（当前模式）', tier: 'decorative', ...rawMenu };
  // 同一 popper 里的普通项作为对照（必须保持达标）
  const rawItem = await page.evaluate(([s]) => measure(s, 'normal'), ['.el-dropdown-menu__item:not(.is-disabled)']);
  results[theme]['dropdown-item'] = { page: '/dashboard(主题下拉)', sel: '.el-dropdown-menu__item:not(.is-disabled)', desc: '主题菜单普通项（对照）', tier: 'normal', ...rawItem };
  await ctx.close();
}

// 移动抽屉版本号（390px）
for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN', colorScheme: theme });
  await ctx.addInitScript((t) => { try { localStorage.setItem('theme', t); } catch (e) {} }, theme);
  await ctx.addInitScript('window.measure = ' + measure.toString() + ';');
  const page = await ctx.newPage();
  await login(page).catch(() => {});
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  for (const t of ['.mobile-menu-btn', '.menu-toggle', 'button[aria-label*="菜单"]', '.header-left .el-button']) {
    if (await page.$(t)) { await page.click(t).catch(() => {}); break; }
  }
  await page.waitForTimeout(1000);
  await page.addStyleTag({ content: '*{transition:none !important;animation:none !important}' });
  if (VARIANT === 'before') await page.addStyleTag({ content: theme === 'dark' ? BEFORE_DARK : BEFORE_LIGHT });
  await page.waitForTimeout(120);
  const rawDrawer = await page.evaluate(([s]) => measure(s, 'normal'), ['.mobile-version-info']);
  results[theme]['drawer-version'] = { page: '/dashboard@390(抽屉)', sel: '.mobile-version-info', desc: '移动抽屉版本号（修饰性）', tier: 'decorative', ...rawDrawer };
  await ctx.close();
}

await browser.close();

const keys = [...new Set(Object.values(results).flatMap((t) => Object.keys(t)))];
const rows = keys.map((k) => {
  const out = { key: k };
  for (const theme of ['light', 'dark']) {
    const m = results[theme][k];
    if (!m || !m.found) { out[theme] = { ratio: null, note: m && m.hidden ? 'hidden' : 'not-found' }; continue; }
    const th = threshold(m, m.tier);
    const cr = ratio(m.fg, m.bg);
    out[theme] = { ratio: cr, fg: m.fg, bg: m.bg, fontSize: m.fontSize, fontWeight: m.fontWeight, threshold: th, pass: cr >= th, desc: m.desc, page: m.page, sel: m.sel, text: m.text };
  }
  return out;
});

fs.mkdirSync(OUT_DIR, { recursive: true });
fs.writeFileSync(OUT_DIR + '/' + LABEL + '.json', JSON.stringify({ base: BASE, label: LABEL, variant: VARIANT, at: stamp, rows }, null, 2));

const pad = (s, n) => String(s == null ? '-' : s).padEnd(n);
const cell = (o) => (o.ratio == null ? o.note : o.ratio + (o.pass ? ' PASS' : ' FAIL'));
console.log('=== ' + LABEL + ' (variant=' + VARIANT + ') | ' + BASE + ' | ' + stamp + ' ===');
for (const r of rows) {
  console.log(pad(r.key, 23) + 'L ' + pad(cell(r.light), 13) + 'D ' + pad(cell(r.dark), 13) + ((r.light.page || r.dark.page) || ''));
}
const fails = rows.filter((r) => (r.light.ratio != null && !r.light.pass) || (r.dark.ratio != null && !r.dark.pass));
console.log('\nFAIL 数: ' + fails.length + (fails.length ? ' -> ' + fails.map((f) => f.key + (f.light.ratio != null && !f.light.pass ? '[light]' : '') + (f.dark.ratio != null && !f.dark.pass ? '[dark]' : '')).join(', ') : ''));
const missing = rows.filter((r) => r.light.ratio == null || r.dark.ratio == null);
if (missing.length) console.log('未取到: ' + missing.map((m) => m.key + '(' + (m.light.note || m.dark.note) + ')').join(', '));
