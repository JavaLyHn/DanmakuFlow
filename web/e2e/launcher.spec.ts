import { expect, test } from '@playwright/test'

test('ordinary pages hide the launcher panel', async ({ page }) => {
  await page.route('**/api/local/session', route => route.fulfill({ status: 404 }))
  await page.goto('/')
  await expect(page.getByRole('region', { name: '本机后端' })).toHaveCount(0)
})

test('explicit stop pauses room actions while preserving socket state', async ({ page }) => {
  const services = ['mysql', 'redis', 'kafka', 'consumer', 'server'].map(id => ({ id, label: id, phase: 'ready', owned: id !== 'mysql' }))
  const snapshot = { busy: false, operation: '', phase: 'ready', progress: '已就绪', canStart: true, canStop: true, port: 8080, services }
  let current = snapshot
  let token = ''
  await page.route('**/api/local/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/session')) return route.fulfill({ json: { kind: 'danmakuflow-launcher', token: 'test-token', port: 8080 } })
    if (path.endsWith('/stop')) { token = route.request().headers()['x-danmakuflow-token']; current = { ...snapshot, busy: true, operation: 'stop', progress: '正在停止' }; return route.fulfill({ status: 202, json: current }) }
    return route.fulfill({ json: current })
  })
  await page.routeWebSocket(/.*/, () => {})
  await page.setViewportSize({ width: 375, height: 800 })
  await page.goto('/')
  await page.getByRole('button', { name: '连接房间', exact: true }).click()
  await expect(page.getByRole('button', { name: '断开连接', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: '停止本次启动的服务' }).click()
  expect(token).toBe('test-token')
  await expect(page.getByRole('button', { name: '发送', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '点赞', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '断开连接', exact: true })).toBeEnabled()
  await expect(page.getByText('非本次启动').first()).toBeVisible()
})

test('partial startup can retry stopping owned services and preserve external MySQL', async ({ page }) => {
  const services = ['mysql', 'redis', 'kafka', 'consumer', 'server'].map(id => ({ id, label: id, phase: id === 'server' ? 'failed' : 'ready', owned: id !== 'mysql' }))
  let state = { busy: false, operation: '', phase: 'failed', progress: '启动未完成', error: 'server failed', canStart: true, canStop: true, port: 8080, services }
  let attempts = 0
  await page.route('**/api/local/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/session')) return route.fulfill({ json: { kind: 'danmakuflow-launcher', token: 't', port: 8080 } })
    if (path.endsWith('/stop')) {
      attempts++
      if (attempts === 1) { state = { ...state, phase: 'stop_failed', error: 'stop failed' }; return route.fulfill({ status: 409, json: { error: 'stop failed' } }) }
      state = { ...state, phase: 'partial', error: '', progress: '已有实例保留运行', canStop: false, services: services.map(service => ({ ...service, phase: service.owned ? 'stopped' : 'ready' })) }
      return route.fulfill({ status: 202, json: state })
    }
    return route.fulfill({ json: state })
  })
  await page.setViewportSize({ width: 375, height: 800 })
  await page.goto('/')
  await expect(page.getByText('server failed')).toBeVisible()
  await page.getByRole('button', { name: '停止本次启动的服务' }).click()
  await expect(page.getByText('stop failed')).toBeVisible()
  await page.getByRole('button', { name: '重试停止' }).click()
  await expect(page.getByText('已有实例保留运行')).toBeVisible()
  await expect(page.locator('.local-services li').filter({ hasText: 'mysql' })).toContainText('非本次启动')
  await page.screenshot({ path: test.info().outputPath('launcher-narrow.png'), fullPage: true })
})


test('failed stop blocks only the affected owned server', async ({ page }) => {
  let owned = true
  const state = () => ({ busy: false, operation: '', phase: 'stop_failed', progress: '停止未完成', canStart: false, canStop: true, port: 8080, services: [{ id: 'server', label: '弹幕服务', phase: 'failed', owned }] })
  await page.route('**/api/local/**', route => new URL(route.request().url()).pathname.endsWith('/session')
    ? route.fulfill({ json: { kind: 'danmakuflow-launcher', token: 't', port: 8080 } })
    : route.fulfill({ json: state() }))
  await page.goto('/')
  await expect(page.getByRole('button', { name: '重试停止' })).toBeEnabled()
  await expect(page.getByRole('button', { name: '连接房间', exact: true })).toBeDisabled()
  await page.getByLabel('服务端端口').fill('8081')
  await expect(page.getByRole('button', { name: '连接房间', exact: true })).toBeEnabled()
  owned = false
  await page.reload()
  await expect(page.getByRole('button', { name: '重试停止' })).toBeVisible()
  await expect(page.getByRole('button', { name: '连接房间', exact: true })).toBeEnabled()
})
