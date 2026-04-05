# FAQ

## 1. How do I add my node to CoreScope?

Go to the **Home** page, search for your node by name or public key, and click **+ Claim**. Your node appears on the dashboard with live status.

## 2. Why does my node show as "Silent"?

Your node hasn't been heard by any observer within the configured threshold. For companions, the default is 24 hours. For repeaters, it's 72 hours. Check that your node is advertising and within range of an observer. See [Configuration](configuration.md) for threshold settings.

## 3. What's the difference between "Last seen" and "Last heard"?

**Last seen** updates only when a node sends an advertisement. **Last heard** updates on *any* traffic from that node. CoreScope uses whichever is more recent for status calculations.

## 4. Why can't I read channel messages?

You need the channel encryption key in your `config.json`. See [Channels](channels.md) for how to configure `channelKeys`.

## 5. What do the packet types mean?

| Type | Meaning |
|------|---------|
| Advert | Node announcing itself to the mesh |
| Channel Msg | Group message on a named channel |
| Direct Msg | Private message between two nodes |
| ACK | Acknowledgment of a received packet |
| Request | Query sent to the mesh |
| Response | Reply to a request |
| Trace | Route tracing packet |
| Path | Path discovery/announcement |

## 6. How do I filter packets by a specific node?

On the [Packets](packets.md) page, use the filter bar and type `from:NodeName` or click a node's name anywhere in the UI to jump to its packets.

## 7. Why do some nodes appear faded on the map?

Faded markers indicate **stale** nodes — they haven't been heard recently. The threshold depends on the node's role.

## 8. Can I run CoreScope without MQTT?

Yes. You can POST packets directly to the `/api/packets` endpoint using the API key. However, MQTT is the standard way to ingest data from mesh observers.

## 9. How do I change the map's default location?

Set `mapDefaults.center` and `mapDefaults.zoom` in your `config.json`. See [Configuration](configuration.md).

## 10. How do I share a link to a specific packet or view?

CoreScope uses URL hashes for deep linking. Copy the URL from your browser — it includes the current page, filters, and selected items. Examples:

- `#/packets/abc123` — a specific packet
- `#/analytics?tab=collisions` — the hash issues tab
- `#/nodes/pubkey123` — a specific node's detail page
