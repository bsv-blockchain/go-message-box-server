/**
 * Integration tests for the device and permission endpoints.
 *
 * MessageBoxClient does not cover these routes, so this suite drives them over
 * AuthFetch directly (BRC-31 mutual auth, the same thing the client uses). Run
 * it against each storage backend — the responses must be identical.
 *
 * Prerequisites:
 *   1. Start the Go server: cd ../; go run ./cmd/server
 *   2. Run tests: npx jest
 */

import { PrivateKey, ProtoWallet, AuthFetch } from '@bsv/sdk'

const SERVER_HOST = process.env.MESSAGEBOX_HOST || 'http://localhost:8080'

// A fresh recipient identity per run, so these assertions own their state.
const recipientKey = PrivateKey.fromRandom()
const senderKey = PrivateKey.fromRandom()
const senderIdentityKey = senderKey.toPublicKey().toString()

const fetcher = new AuthFetch(new ProtoWallet(recipientKey) as any)

// A second profile on the same install: it has its own identity and registers
// the same FCM token the first one does.
const otherProfileFetcher = new AuthFetch(new ProtoWallet(PrivateKey.fromRandom()) as any)

async function call(method: string, path: string, body?: unknown, as: AuthFetch = fetcher) {
  const res = await as.fetch(`${SERVER_HOST}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {})
  })
  const text = await res.text()
  let json: any = null
  try { json = JSON.parse(text) } catch { /* non-JSON body stays null */ }
  return { status: res.status, json }
}

describe('Go MessageBox Server — Devices', () => {
  // The registration's numeric ID. @bsv/message-box-client >= 2.5 rejects a
  // registration response without it, and a device record without it as id.
  let registrationId: number

  test('should register a device', async () => {
    const { status, json } = await call('POST', '/registerDevice', {
      fcmToken: 'tok-integration-1',
      deviceId: 'dev-1',
      platform: 'ios'
    })

    expect(status).toBe(200)
    expect(json.status).toBe('success')
    expect(typeof json.message).toBe('string')
    expect(Number.isSafeInteger(json.deviceId) && json.deviceId >= 1).toBe(true)
    registrationId = json.deviceId
  })

  test('should list the registered device with its registration id', async () => {
    const { status, json } = await call('GET', '/devices')

    expect(status).toBe(200)
    expect(json.devices).toHaveLength(1)
    expect(json.devices[0]).toMatchObject({
      id: registrationId,
      deviceId: 'dev-1',
      platform: 'ios',
      active: true
    })
    // The token is truncated to its last 10 characters in responses.
    expect(json.devices[0].fcmToken).toBe('...egration-1')
  })

  test('should update rather than duplicate on re-registration', async () => {
    await call('POST', '/registerDevice', {
      fcmToken: 'tok-integration-1',
      deviceId: 'dev-2',
      platform: 'android'
    })

    const { json } = await call('GET', '/devices')
    expect(json.devices).toHaveLength(1)
    expect(json.devices[0].id).toBe(registrationId)
    expect(json.devices[0].deviceId).toBe('dev-2')
    expect(json.devices[0].platform).toBe('android')
  })

  test('should reject an unknown platform', async () => {
    const { status, json } = await call('POST', '/registerDevice', {
      fcmToken: 'tok-integration-2',
      platform: 'nope'
    })

    expect(status).toBe(400)
    expect(json.code).toBe('ERR_INVALID_PLATFORM')
  })

  // A wallet with several profiles has one FCM token and registers it once per
  // profile. The second registration must not take it from the first.
  test('should let a second identity register the same token', async () => {
    const { status, json } = await call('POST', '/registerDevice', {
      fcmToken: 'tok-integration-1',
      platform: 'ios'
    }, otherProfileFetcher)

    expect(status).toBe(200)
    expect(Number.isSafeInteger(json.deviceId) && json.deviceId >= 1).toBe(true)
    expect(json.deviceId).not.toBe(registrationId)

    const mine = await call('GET', '/devices')
    expect(mine.json.devices).toHaveLength(1)
    expect(mine.json.devices[0]).toMatchObject({ id: registrationId, active: true })

    const theirs = await call('GET', '/devices', undefined, otherProfileFetcher)
    expect(theirs.json.devices).toHaveLength(1)
    expect(theirs.json.devices[0]).toMatchObject({ id: json.deviceId, active: true })
  })

  test('should unregister only the callers registration of a token', async () => {
    const { status, json } = await call('POST', '/unregisterDevice', {
      fcmToken: 'tok-integration-1'
    })

    expect(status).toBe(200)
    expect(json.status).toBe('success')

    const mine = await call('GET', '/devices')
    expect(mine.json.devices).toHaveLength(0)

    const theirs = await call('GET', '/devices', undefined, otherProfileFetcher)
    expect(theirs.json.devices).toHaveLength(1)
    expect(theirs.json.devices[0].active).toBe(true)
  })

  test('should succeed when unregistering a token that is not registered', async () => {
    const again = await call('POST', '/unregisterDevice', { fcmToken: 'tok-integration-1' })
    expect(again.status).toBe(200)
    expect(again.json.status).toBe('success')

    const never = await call('POST', '/unregisterDevice', { fcmToken: 'tok-never-registered' })
    expect(never.status).toBe(200)
  })

  test('should reject unregistering without a token', async () => {
    const { status, json } = await call('POST', '/unregisterDevice', { fcmToken: '' })

    expect(status).toBe(400)
    expect(json.code).toBe('ERR_INVALID_FCM_TOKEN')
  })
})

describe('Go MessageBox Server — Permissions', () => {
  test('should set a box-wide permission', async () => {
    const { status } = await call('POST', '/permissions/set', {
      messageBox: 'inbox',
      recipientFee: 25
    })
    expect(status).toBe(200)
  })

  test('should set a sender-specific permission', async () => {
    const { status } = await call('POST', '/permissions/set', {
      messageBox: 'inbox',
      sender: senderIdentityKey,
      recipientFee: -1
    })
    expect(status).toBe(200)
  })

  test('should get the box-wide permission with a null sender', async () => {
    const { status, json } = await call('GET', '/permissions/get?messageBox=inbox')

    expect(status).toBe(200)
    expect(json.permission).toMatchObject({
      sender: null,
      messageBox: 'inbox',
      recipientFee: 25,
      status: 'payment_required'
    })
  })

  test('should get the sender-specific permission as blocked', async () => {
    const { status, json } = await call(
      'GET', `/permissions/get?messageBox=inbox&sender=${senderIdentityKey}`
    )

    expect(status).toBe(200)
    expect(json.permission).toMatchObject({
      sender: senderIdentityKey,
      recipientFee: -1,
      status: 'blocked'
    })
  })

  test('should list permissions in contract order', async () => {
    await call('POST', '/permissions/set', { messageBox: 'notifications', recipientFee: 5 })

    const { status, json } = await call('GET', '/permissions/list?limit=10')

    expect(status).toBe(200)
    expect(json.totalCount).toBe(3)
    // Message box ascending, with the box-wide row before sender-specific ones.
    expect(json.permissions.map((p: any) => `${p.message_box}/${p.sender ?? '*'}`)).toEqual([
      'inbox/*',
      `inbox/${senderIdentityKey}`,
      'notifications/*'
    ])
  })

  test('should page without changing the total', async () => {
    const { json } = await call('GET', '/permissions/list?limit=2&offset=1')

    expect(json.totalCount).toBe(3)
    expect(json.permissions).toHaveLength(2)
  })

  test('should accept both sort directions', async () => {
    for (const order of ['asc', 'desc']) {
      const { status, json } = await call('GET', `/permissions/list?limit=10&createdAtOrder=${order}`)
      expect(status).toBe(200)
      expect(json.totalCount).toBe(3)
    }
  })

  test('should filter by messageBox', async () => {
    const { json } = await call('GET', '/permissions/list?messageBox=notifications&limit=10')

    expect(json.totalCount).toBe(1)
    expect(json.permissions[0].message_box).toBe('notifications')
  })

  test('should reject an out-of-range limit', async () => {
    const { status, json } = await call('GET', '/permissions/list?limit=0')

    expect(status).toBe(400)
    expect(json.code).toBe('ERR_INVALID_LIMIT')
  })
})

describe('Go MessageBox Server — Quote', () => {
  test('should quote a single recipient', async () => {
    const { status, json } = await call(
      'GET', `/permissions/quote?recipient=${senderIdentityKey}&messageBox=notifications`
    )

    expect(status).toBe(200)
    expect(json.quote).toMatchObject({ deliveryFee: 10, recipientFee: 10 })
  })

  test('should persist the smart default it quoted', async () => {
    // The quote above found no permission row for the quoted recipient, so the
    // smart default must have been written on their behalf.
    const senderFetcher = new AuthFetch(new ProtoWallet(senderKey) as any)
    const res = await senderFetcher.fetch(`${SERVER_HOST}/permissions/list?limit=10`, { method: 'GET' })
    const json = await res.json()

    expect(json.totalCount).toBe(1)
    expect(json.permissions[0]).toMatchObject({
      message_box: 'notifications',
      recipient_fee: 10,
      sender: null
    })
  })
})
