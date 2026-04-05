# Channels

The Channels page shows decrypted MeshCore channel messages — like a group chat viewer for your mesh.

[Screenshot: channels page with message list]

## What are channels?

MeshCore nodes can send messages on named channels (like `#LongFast` or `#test`). These are group messages broadcast through the mesh. Any observer that hears the packet captures it.

CoreScope can decrypt and display these messages if you provide the channel encryption key.

## How it works

1. Observers capture encrypted channel packets from the mesh
2. CoreScope matches the packet's channel hash to a known channel name
3. If a decryption key is configured, the message content is decrypted and displayed
4. Without a key, you'll see the packet metadata but not the message text

## Viewing messages

Select a channel from the list on the left. Messages appear in chronological order on the right.

Each message shows:
- **Sender** — node name or hash
- **Text** — decrypted message content
- **Observer** — which observer captured it
- **Time** — when it was received

The message list auto-scrolls to show new messages as they arrive via WebSocket.

## Channel keys

To decrypt messages, add channel keys to your `config.json`:

```json
{
  "channelKeys": {
    "public": "8b3387e9c5cdea6ac9e5edbaa115cd72"
  }
}
```

The key name (e.g., `"public"`) is a label for your reference. The value is the 16-byte hex encryption key for that channel.

See [Configuration](configuration.md) for details on `channelKeys` and `hashChannels`.

## Hash channels

The `hashChannels` config lists channel names that CoreScope should try to match by hash:

```json
{
  "hashChannels": ["#LongFast", "#test", "#sf"]
}
```

CoreScope computes the hash of each name and matches incoming packets to identify which channel they belong to.

## Region filter

Channels respect the region filter. Select a region to see only messages captured by observers in that area.

## Tips

- The default MeshCore "public" channel key is well-known — most community meshes use it
- If messages appear but show garbled text, your key may be wrong
- Not all packets are channel messages — only type "Channel Msg" (GRP_TXT) appears here
