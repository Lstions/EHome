import { test, expect, type Page } from '@playwright/test';

// ── Login Helper ──────────────────────────────────────────────────

async function login(page: Page): Promise<void> {
  await page.goto('/login');
  await page.waitForSelector('.el-input, form, .el-form', { timeout: 10000 });
  await page.locator('input').first().fill('admin');
  await page.locator('input[type="password"]').fill('admin123');
  await page.locator('button.el-button--primary, button[type="submit"], button:has-text("登录")').first().click();
  await page.waitForURL(/\/(dashboard|node|edge-device)/, { timeout: 15000 });
}

// ── Device Management Flow E2E Tests ──────────────────────────────

test.describe('设备管理流程', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('节点列表页 - 渲染验证', async ({ page }) => {
    await page.goto('/node');
    await page.waitForSelector('.el-table, .el-card, .node-list', { timeout: 10000 });

    // 页面应有表格或卡片
    const tableOrCard = page.locator('.el-table, .el-card');
    const count = await tableOrCard.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('节点详情页 - 导航到详情', async ({ page }) => {
    await page.goto('/node');
    await page.waitForSelector('.el-table, .el-card, .node-list', { timeout: 10000 });

    // 尝试从列表页点击进入详情
    const detailLink = page.locator('a[href*="/node/"], .el-table__row, button:has-text("详情")').first();
    const linkCount = await detailLink.count();

    if (linkCount === 0) {
      test.skip(true, '节点列表无数据，无法测试详情页导航');
    }

    await detailLink.click();
    await page.waitForURL(/\/node\/\d+/, { timeout: 10000 });

    // 详情页应有内容容器
    await page.waitForSelector('.el-card, .el-descriptions, .collector-detail, .node-detail', { timeout: 5000 });
    expect(page.url()).toMatch(/\/node\/\d+/);
  });

  test('边缘设备列表页 - 渲染验证', async ({ page }) => {
    await page.goto('/edge-device');
    await page.waitForSelector('.el-table, .el-card, .edge-device', { timeout: 10000 });

    // 页面应有表格或卡片
    const tableOrCard = page.locator('.el-table, .el-card');
    const count = await tableOrCard.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('通道管理页 - 渲染验证', async ({ page }) => {
    await page.goto('/channel');
    await page.waitForSelector('.el-table, .el-card, .channel', { timeout: 10000 });

    // 页面应有表格或卡片
    const tableOrCard = page.locator('.el-table, .el-card');
    const count = await tableOrCard.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('数据面板页 - 渲染验证', async ({ page }) => {
    await page.goto('/data');
    await page.waitForSelector('.el-card, .data-panel, .el-form', { timeout: 10000 });

    // 页面应有卡片或表单
    const contentEl = page.locator('.el-card, .data-panel, .el-form');
    const count = await contentEl.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('侧边栏导航 - dashboard → node → edge-device → channel → data', async ({ page }) => {
    // 先导航到 dashboard 确保侧边栏已渲染
    await page.goto('/dashboard');
    await page.waitForSelector('.sidebar, .el-menu, .el-aside', { timeout: 10000 });

    // 验证侧边栏菜单项存在
    const menuItems = page.locator('.el-menu-item');
    const menuCount = await menuItems.count();
    expect(menuCount).toBeGreaterThan(0);

    // 逐个导航验证
    const routes = [
      { path: '/dashboard', title: '仪表盘' },
      { path: '/node', title: '节点' },
      { path: '/edge-device', title: '边缘设备' },
      { path: '/channel', title: '通道管理' },
      { path: '/data', title: '数据面板' },
    ];

    for (const route of routes) {
      // 尝试点击侧边栏菜单项导航
      const menuItem = page.locator(`.el-menu-item:has-text("${route.title}")`).first();
      const itemExists = await menuItem.count();

      if (itemExists > 0) {
        await menuItem.click();
      } else {
        // 如果侧边栏菜单项不可用（如折叠状态），直接导航
        await page.goto(route.path);
      }

      // 等待页面加载完成
      await page.waitForSelector('.el-main, .main-content, body', { timeout: 10000 });

      // 验证 URL 已更新
      expect(page.url()).toContain(route.path);
    }
  });
});
