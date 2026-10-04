# oi_burner — protocol

Burner chat for two people. A rendezvous server introduces them, then gets out
of the way. Messages go peer-to-peer over UDP and never touch the server, so
there is nothing server-side to log.

## Client features

- Login screen: room code + password
- Chat screen: your messages in blue, theirs in pink
- `esc` — close the connection and quit
- `/burn` or `ctrl+b` — tell the peer to destroy the conversation, wipe
  locally, exit, and clear the terminal scrollback

## The handshake

Both clients send the same UDP packet to the server and wait.

**Client → server** (JSON, one datagram):

```json
{ "room_id": "whatever-they-typed", "password": "whatever-they-typed" }
```

**Server → client** (JSON, one datagram each):

```json
{ "peer": "203.0.113.9:51820" }
```

or on failure:

```json
{ "error": "some error here" }
```

`peer` is the **public** ip:port the server observed on the *other* client's
packet

## After the handshake

The server is out. The two clients talk directly on the **same socket** they
used for the handshake

Peer-to-peer datagrams are one byte of type, then payload:

| Byte | Meaning |
|------|---------|
| `m`  | chat message, rest of the datagram is UTF-8 text |
| `b`  | burn — wipe and exit, sent 3x since UDP drops things |
