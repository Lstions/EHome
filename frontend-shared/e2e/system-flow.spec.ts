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

// ── System Feature Flow E2E Tests ─────────────────────────────────

test.describe('系统功能流程', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('系统监控页 - 渲染验证', async ({ page }) => {
    await page.goto('/monitor');
    await page.waitForSelector('.el-card, .monitor, .el-statistic', { timeout: 10000 });

    // 页面应有监控相关内容
    const contentEl = page.locator('.el-card, .monitor, .el-statistic, [class*="monitor"]');
    const count = await contentEl.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('固件管理页 - 渲染验证', async ({ page }) => {
    await page.goto('/firmware');
    await page.waitForSelector('.el-table, .el-card, .firmware', { timeout: 10000 });

    // 页面应有表格或卡片
    const contentEl = page.locator('.el-table, .el-card, [class*="firmware"]');
    const count = await contentEl.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('个人设置页 - 渲染验证', async ({ page }) => {
    await page.goto('/profile');
    await page.waitForSelector('.el-card, .el-descriptions, .profile', { timeout: 10000 });

    // 页面应有个人资料相关内容
    const contentEl = page.locator('.el-card, .el-descriptions, .profile, [class*="profile"]');
    const count = await contentEl.count();
    expect(count).toBeGreaterThan(0);

    // body 应有内容
    const bodyText = await page.locator('body').innerText();
    expect(bodyText.length).toBeGreaterThan(50);
  });

  test('404页面 - 访问不存在路由 → 显示404', async ({ page }) => {
    await page.goto('/nonexistent-page-404-test');

    // 应显示404错误页 (catch-all 路由)
    await page.waitForSelector('.error-page.not-found, .error-code', { timeout: 10000 });

    // 验证404错误码可见
    const errorCode = page.locator('.error-code');
    await expect(errorCode).toBeVisible({ timeout: 5000 });
    await expect(errorCode).toHaveText('404');

    // 验证错误标题
    const errorTitle = page.locator('.error-title');
    await expect(errorTitle).toBeVisible({ timeout: 5000 });
    await expect(errorTitle).toHaveText('页面不存在');
  });

  test('403页面 - 直接访问 /403 路由 → 显示禁止访问', async ({ page }) => {
    // 直接访问 /403 路由
    await page.goto('/403');

    // 应显示403错误页
    await page.waitForSelector('.error-page.forbidden, .error-code', { timeout: 10000 });

    // 验证403错误码可见
    const errorCode = page.locator('.error-code');
    await expect(errorCode).toBeVisible({ timeout: 5000 });
    await expect(errorCode).toHaveText('403');

    // 验证错误标题
    const errorTitle = page.locator('.error-title');
    await expect(errorTitle).toBeVisible({ timeout: 5000 });
    await expect(errorTitle).toHaveText('无权访问');
  });
});
