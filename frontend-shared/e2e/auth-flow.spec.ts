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

// ── Auth Flow E2E Tests ───────────────────────────────────────────

test.describe('认证流程', () => {

  test('登录页 - 标题、输入框、按钮可见', async ({ page }) => {
    await page.goto('/login');
    await page.waitForSelector('.login-box, .el-form', { timeout: 10000 });

    // 品牌标题可见
    await expect(page.locator('.brand-name')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.brand-name')).toHaveText('EHomeSystem');

    // 用户名输入框可见
    await expect(page.locator('input').first()).toBeVisible({ timeout: 5000 });

    // 密码输入框可见
    await expect(page.locator('input[type="password"]')).toBeVisible({ timeout: 5000 });

    // 登录按钮可见
    const loginBtn = page.locator('button.el-button--primary, button[type="submit"], button:has-text("登录")').first();
    await expect(loginBtn).toBeVisible({ timeout: 5000 });
  });

  test('正确凭证登录 → 跳转 dashboard', async ({ page }) => {
    await page.goto('/login');
    await page.waitForSelector('.el-input, form, .el-form', { timeout: 10000 });

    await page.locator('input').first().fill('admin');
    await page.locator('input[type="password"]').fill('admin123');
    await page.locator('button.el-button--primary, button[type="submit"], button:has-text("登录")').first().click();

    // 登录成功后应跳转到 dashboard 或其他已认证页面
    await page.waitForURL(/\/(dashboard|node|edge-device)/, { timeout: 15000 });
    expect(page.url()).not.toContain('/login');
  });

  test('错误密码登录失败 → 显示错误提示', async ({ page }) => {
    await page.goto('/login');
    await page.waitForSelector('.el-input, form, .el-form', { timeout: 10000 });

    await page.locator('input').first().fill('wrong_user');
    await page.locator('input[type="password"]').fill('wrong_password_123');
    await page.locator('button.el-button--primary, button[type="submit"], button:has-text("登录")').first().click();

    // 登录失败后应显示错误提示 (el-alert type=error)
    await page.waitForSelector('.el-alert--error, .el-message--error', { timeout: 10000 });

    // 仍在登录页
    expect(page.url()).toContain('/login');
  });

  test('登出 → 返回登录页', async ({ page }) => {
    await login(page);

    // 点击用户菜单下拉
    const userMenu = page.locator('.user-menu, .el-dropdown').first();
    await expect(userMenu).toBeVisible({ timeout: 5000 });
    await userMenu.click();

    // 点击"退出登录"
    const logoutItem = page.locator('.el-dropdown-menu__item:has-text("退出登录"), .el-dropdown-menu__item:has-text("退出")').first();
    await page.waitForSelector('.el-dropdown-menu', { timeout: 5000 });

    // 可能出现确认弹窗
    const logoutBtn = page.locator('.el-dropdown-menu__item:has-text("退出登录"), .el-dropdown-menu__item:has-text("退出")').first();
    await logoutBtn.click();

    // 可能有确认对话框
    const confirmBtn = page.locator('.el-message-box .el-button--primary, .el-dialog .el-button--primary, .el-popconfirm .el-button--primary');
    if (await confirmBtn.count() > 0) {
      await confirmBtn.first().click();
    }

    // 应跳转到登录页
    await page.waitForURL(/\/login/, { timeout: 15000 });
    expect(page.url()).toContain('/login');
  });

  test('未登录访问受保护路由 → 重定向 login', async ({ page }) => {
    // 直接访问 dashboard，应被重定向到登录页
    await page.goto('/dashboard');

    await page.waitForURL(/\/login/, { timeout: 10000 });
    expect(page.url()).toContain('/login');

    // 应携带 redirect 参数
    const url = new URL(page.url());
    const redirect = url.searchParams.get('redirect');
    expect(redirect).toContain('/dashboard');
  });

  test('已登录访问 /login → 重定向 dashboard', async ({ page }) => {
    await login(page);

    // 已登录状态下访问 /login 应重定向到 dashboard
    await page.goto('/login');
    await page.waitForURL(/\/(dashboard|node|edge-device)/, { timeout: 10000 });
    expect(page.url()).not.toContain('/login');
  });
});
