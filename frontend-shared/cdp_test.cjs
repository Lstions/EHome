const { chromium } = require('playwright');
const fs = require('fs');

(async () => {
  const browser = await chromium.launch({
    executablePath: '/usr/bin/chromium-browser',
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-gpu']
  });

  const context = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await context.newPage();

  const shots = [];

  // 1. Login page
  console.log('[1] Login page...');
  await page.goto('http://localhost:5174', { waitUntil: 'networkidle', timeout: 30000 });
  await page.screenshot({ path: '/tmp/cdp_01_login.png' });
  shots.push('/tmp/cdp_01_login.png');

  // 2. Try login
  console.log('[2] Logging in...');
  try {
    const userInput = page.locator('input[type="text"]').first();
    const passInput = page.locator('input[type="password"]').first();
    await userInput.fill('admin');
    await passInput.fill('admin123');
    await page.locator('button[type="submit"], button:has-text("登"), .el-button--primary').first().click();
    await page.waitForURL('**/nodes**', { timeout: 10000 }).catch(() => {});
    await page.waitForTimeout(2000);
  } catch (e) {
    console.log('  Login attempt:', e.message.substring(0, 100));
  }
  await page.screenshot({ path: '/tmp/cdp_02_dashboard.png' });
  shots.push('/tmp/cdp_02_dashboard.png');

  // 3. Node list
  console.log('[3] Node list...');
  await page.goto('http://localhost:5174/nodes', { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(2000);
  await page.screenshot({ path: '/tmp/cdp_03_node_list.png' });
  shots.push('/tmp/cdp_03_node_list.png');

  // 4. Node detail - try finding the S3 device link
  console.log('[4] Node detail...');
  try {
    const s3Link = page.getByText('30EDA0A9A808').first();
    if (await s3Link.isVisible()) {
      await s3Link.click();
      await page.waitForTimeout(3000);
    } else {
      await page.goto('http://localhost:5174/nodes/2', { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
      await page.waitForTimeout(3000);
    }
  } catch (e) {
    await page.goto('http://localhost:5174/nodes/2', { waitUntil: 'networkidle', timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(3000);
  }
  await page.screenshot({ path: '/tmp/cdp_04_node_detail.png' });
  shots.push('/tmp/cdp_04_node_detail.png');

  // 5. Scroll down for DMA section
  console.log('[5] DMA section (scroll)...');
  await page.evaluate(() => window.scrollBy(0, 500));
  await page.waitForTimeout(1500);
  await page.screenshot({ path: '/tmp/cdp_05_dma_section.png' });
  shots.push('/tmp/cdp_05_dma_section.png');

  // 6. More scroll for hardware
  console.log('[6] Hardware resources (scroll)...');
  await page.evaluate(() => window.scrollBy(0, 500));
  await page.waitForTimeout(1500);
  await page.screenshot({ path: '/tmp/cdp_06_hw_resources.png' });
  shots.push('/tmp/cdp_06_hw_resources.png');

  // 7. Even more scroll
  console.log('[7] More content (scroll)...');
  await page.evaluate(() => window.scrollBy(0, 500));
  await page.waitForTimeout(1500);
  await page.screenshot({ path: '/tmp/cdp_07_more.png' });
  shots.push('/tmp/cdp_07_more.png');

  // Summary
  console.log('\nAll screenshots:');
  for (const f of shots) {
    try {
      const stat = fs.statSync(f);
      console.log('  ' + f + ' (' + stat.size + ' bytes)');
    } catch (e) {
      console.log('  ' + f + ' MISSING');
    }
  }

  await browser.close();
  console.log('\nCDP session complete.');
})();
