import { expect, test, type Page, type WebSocketRoute } from '@playwright/test'

// Route every socket: these tests never connect to the Go app or infrastructure.
async function mockRoom(page: Page) {
  const sockets: WebSocketRoute[] = []
  const sent: unknown[] = []
  await page.routeWebSocket(/.*/, socket => {
    sockets.push(socket)
    socket.onMessage(message => sent.push(JSON.parse(message.toString())))
  })
  await page.goto('/')
  return { sockets, sent }
}

async function connect(page: Page, sockets: WebSocketRoute[], count = 1) {
  await page.getByRole('button', { name: '连接房间', exact: true }).click()
  await expect.poll(() => sockets.length).toBe(count)
  await expect(page.getByRole('button', { name: '断开连接', exact: true })).toBeEnabled()
  await expect(page.getByLabel('弹幕内容', { exact: true })).toBeEnabled()
  return sockets[count - 1]!
}

test('defaults, generated UID, exact input semantics and no optimistic updates', async ({ page }) => {
  const { sockets, sent } = await mockRoom(page)
  const draft = page.getByLabel('弹幕内容', { exact: true })
  const log = page.getByRole('log', { name: '房间弹幕' })
  await expect(page.getByLabel('服务端端口', { exact: true })).toHaveValue('8080')
  await expect(page.getByLabel('房间 ID', { exact: true })).toHaveValue('1001')
  await expect(page.getByLabel('用户 ID', { exact: true })).toHaveValue('')
  await expect(draft).toBeDisabled()
  const socket = await connect(page, sockets)
  const url = new URL(socket.url())
  expect(url.origin).toBe('ws://127.0.0.1:8080')
  expect(url.pathname).toBe('/ws')
  expect(url.searchParams.get('room')).toBe('1001')
  const uid = url.searchParams.get('uid')!
  expect(uid).toMatch(/^\d+$/)
  expect(BigInt(uid)).toBeLessThanOrEqual(18446744073709551615n)
  expect(url.searchParams.get('name')).toBe(uid)
  await expect(page.getByLabel('用户 ID', { exact: true })).toHaveValue(uid)
  await expect(page.getByLabel('房间 ID', { exact: true })).toBeDisabled()
  await expect(page.getByTestId('online-count')).toHaveText('—')
  await expect(page.getByTestId('like-count')).toHaveText('—')

  let count = 0
  for (const content of ['hello from browser', ' 1', '1 ', '01', '']) {
    await draft.fill(content)
    await page.getByRole('button', { name: '发送', exact: true }).click()
    await expect.poll(() => sent.length).toBe(++count)
    expect(sent.at(-1)).toMatchObject({ type: 101, data: { content } })
    await expect(draft).toHaveValue('')
  }
  await expect(log.getByText('hello from browser', { exact: true })).toHaveCount(0)
  await draft.fill('1')
  await draft.press('Enter')
  await expect.poll(() => sent.length).toBe(++count)
  expect(sent.at(-1)).toEqual({ type: 103, data: null })
  await draft.fill('未发送的草稿')
  await page.getByRole('button', { name: '点赞', exact: true }).click()
  await expect.poll(() => sent.length).toBe(++count)
  expect(sent.at(-1)).toEqual({ type: 103, data: null })
  await expect(draft).toHaveValue('未发送的草稿')
  await expect(page.getByTestId('like-count')).toHaveText('—')
})

test('independent statistics, uint64 precision and safe text rendering', async ({ page }) => {
  const { sockets } = await mockRoom(page)
  await page.getByLabel('用户 ID', { exact: true }).fill('18446744073709551615')
  const socket = await connect(page, sockets)
  expect(new URL(socket.url()).searchParams.get('uid')).toBe('18446744073709551615')
  socket.send('{"type":102,"data":{"online":9007199254740993,"likes":18446744073709551615}}')
  await expect(page.getByTestId('online-count')).toHaveText('9007199254740993')
  await expect(page.getByTestId('like-count')).toHaveText('18446744073709551615')
  const log = page.getByRole('log', { name: '房间弹幕' })
  const text = '  <img src=x onerror="alert(1)">  原样弹幕  '
  socket.send(JSON.stringify({ type: 101, data: { content: text } }))
  await expect(log).toContainText(text.trim())
  expect(await log.locator('img').count()).toBe(0)
  expect(await log.textContent()).toContain(text)
  socket.send('invalid json')
  socket.send('{"type":999,"data":{}}')
  socket.send(JSON.stringify({ type: 101, data: { content: '后续弹幕仍能收到' } }))
  await expect(log.getByText('后续弹幕仍能收到', { exact: true })).toBeVisible()
})

test('disconnect retains room ownership; a new session starts empty', async ({ page }) => {
  const { sockets } = await mockRoom(page)
  const first = await connect(page, sockets)
  first.send('{"type":101,"data":{"content":"旧房间的消息"}}')
  first.send('{"type":102,"data":{"online":18,"likes":126}}')
  const log = page.getByRole('log', { name: '房间弹幕' })
  await expect(log.getByText('旧房间的消息', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '断开连接', exact: true }).click()
  await expect(page.getByLabel('弹幕内容', { exact: true })).toBeDisabled()
  await page.getByLabel('房间 ID', { exact: true }).fill('2002')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('房间 1001')
  await expect(log.getByText('旧房间的消息', { exact: true })).toBeVisible()
  await expect(page.getByTestId('like-count')).toHaveText('126')
  const next = await connect(page, sockets, 2)
  expect(new URL(next.url()).searchParams.get('room')).toBe('2002')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('房间 2002')
  await expect(log.getByText('旧房间的消息', { exact: true })).toHaveCount(0)
  await expect(page.getByTestId('online-count')).toHaveText('—')
  await expect(page.getByTestId('like-count')).toHaveText('—')
  next.send('{"type":101,"data":{"content":"新房间的消息"}}')
  await expect(log.getByText('新房间的消息', { exact: true })).toBeVisible()
  await expect(page.getByTestId('online-count')).toHaveText('—')
})

test('remote closure disables sends and preserves received messages', async ({ page }) => {
  const { sockets } = await mockRoom(page)
  const socket = await connect(page, sockets)
  socket.send('{"type":101,"data":{"content":"断开前已收到"}}')
  await expect(page.getByRole('log').getByText('断开前已收到', { exact: true })).toBeVisible()
  await socket.close({ code: 1011, reason: '测试关闭原因' })
  await expect(page.getByRole('button', { name: '连接房间', exact: true })).toBeEnabled()
  await expect(page.getByRole('button', { name: '发送', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '点赞', exact: true })).toBeDisabled()
  await expect(page.locator('#df-feedback')).toContainText('测试关闭原因')
  await expect(page.getByRole('log').getByText('断开前已收到', { exact: true })).toBeVisible()
  expect(sockets).toHaveLength(1)
})

test('Chinese input confirmation does not submit a message', async ({ page }) => {
  const { sockets, sent } = await mockRoom(page)
  await connect(page, sockets)
  const draft = page.getByLabel('弹幕内容', { exact: true })
  await draft.fill('正在输入中文')
  await draft.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', isComposing: true, keyCode: 229 })
  await expect(draft).toHaveValue('正在输入中文')
  expect(sent).toHaveLength(0)
  await draft.press('Enter')
  await expect.poll(() => sent.length).toBe(1)
  expect(sent[0]).toMatchObject({ type: 101, data: { content: '正在输入中文' } })
})

test('desktop and narrow layout stay readable with long content', async ({ page }, testInfo) => {
  const { sockets } = await mockRoom(page)
  const socket = await connect(page, sockets)
  socket.send('{"type":102,"data":{"online":18,"likes":126}}')
  for (const content of ['大家好，进入房间了。', '收到这条弹幕了 👋', '同一个房间可以一起聊天。', '试一下点赞。']) {
    socket.send(JSON.stringify({ type: 101, data: { content } }))
  }
  await expect(page.getByRole('log').getByText('试一下点赞。', { exact: true })).toBeVisible()
  await expect(page.getByText('视频画面暂未显示', { exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('desktop.png'), fullPage: true })
  await page.setViewportSize({ width: 320, height: 800 })
  socket.send('{"type":102,"data":{"online":18446744073709551615,"likes":18446744073709551615}}')
  socket.send(JSON.stringify({ type: 101, data: { content: 'x'.repeat(200) } }))
  await expect(page.getByTestId('online-count')).toHaveText('18446744073709551615')
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole('button', { name: '发送', exact: true }).scrollIntoViewIfNeeded()
  await expect(page.getByRole('button', { name: '发送', exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('mobile.png'), fullPage: true })
})
