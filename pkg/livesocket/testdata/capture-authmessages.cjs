// Regenerates ts-authmessages.json: a BRC-103 handshake and one general
// message between two @bsv/sdk Peers over an in-memory transport. Each frame
// is what socket.io carries: JSON.stringify of the AuthMessage handed to
// send(). Nonces and signatures differ on every run; the keys are fixed.
//
//   npm install @bsv/sdk@3.2.1
//   node capture-authmessages.cjs > ts-authmessages.json
const { Peer, ProtoWallet, PrivateKey, Utils } = require('@bsv/sdk')

const frames = []
class Mem {
  constructor () { this.cb = null; this.other = null }
  async send (m) {
    frames.push(JSON.parse(JSON.stringify(m)))
    await this.other.cb(JSON.parse(JSON.stringify(m)))
  }
  async onData (cb) { this.cb = cb }
}

;(async () => {
  const a = new Mem(); const b = new Mem(); a.other = b; b.other = a
  const ka = PrivateKey.fromHex('11'.repeat(32)); const kb = PrivateKey.fromHex('22'.repeat(32))
  const pa = new Peer(new ProtoWallet(ka), a)
  const pb = new Peer(new ProtoWallet(kb), b)
  const got = new Promise(resolve => pb.listenForGeneralMessages(() => resolve()))
  const payload = Utils.toArray(JSON.stringify({ eventName: 'sendMessage', data: { body: 'h\u00e9llo <&> \u{1F642}' } }), 'utf8')
  await pa.toPeer(payload, kb.toPublicKey().toString())
  await got
  // 4-space indent and a final newline: the layout magex format:fix gives JSON
  const fixture = { client: ka.toPublicKey().toString(), server: kb.toPublicKey().toString(), frames }
  process.stdout.write(JSON.stringify(fixture, null, 4) + '\n')
})().catch(e => { console.error(e); process.exit(1) })
