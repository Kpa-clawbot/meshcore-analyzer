# Customizing Your Instance

## Quick Start

1. Open your analyzer in a browser
2. Go to **Tools → Customize**
3. Change colors, branding, home page content
4. Click **💾 Download theme.json**
5. Put the file next to your `config.json` on the server
6. Refresh the page — done

No restart needed. The server picks up changes to `theme.json` on every page load.

## Where Does theme.json Go?

**Next to config.json.** However you deployed, put them side by side.

**Docker:**
```bash
# Add to your docker run command:
-v /path/to/theme.json:/app/theme.json:ro

# Or if you bind-mount the data directory:
# Just put theme.json in that directory
```

**Bare metal / PM2 / systemd:**
```bash
# Same directory as server.js and config.json
cp theme.json /path/to/meshcore-analyzer/
```

Check the server logs on startup — it tells you where it's looking:
```
[theme] Loaded from /app/theme.json
```
or:
```
[theme] No theme.json found. Place it next to config.json or in data/ to customize.
```

## What Can You Customize?

### Branding
```json
{
  "branding": {
    "siteName": "Bay Area Mesh",
    "tagline": "Community LoRa mesh network",
    "logoUrl": "/my-logo.svg",
    "faviconUrl": "/my-favicon.png"
  }
}
```

Logo replaces the 🍄 emoji in the nav bar (renders at 24px height). Favicon replaces the browser tab icon. Use a URL path for files in the `public/` folder, or a full URL for external images.

### Theme Colors (Light Mode)
```json
{
  "theme": {
    "accent": "#ff6b6b",
    "navBg": "#1a1a2e",
    "navText": "#ffffff",
    "background": "#f4f5f7",
    "text": "#1a1a2e",
    "statusGreen": "#22c55e",
    "statusYellow": "#eab308",
    "statusRed": "#ef4444"
  }
}
```

### Theme Colors (Dark Mode)
```json
{
  "themeDark": {
    "accent": "#57f2a5",
    "navBg": "#0a0a1a",
    "background": "#0f0f23",
    "text": "#e2e8f0"
  }
}
```

Only include colors you want to change — everything else stays default.

### All Available Theme Keys

| Key | What It Controls |
|-----|-----------------|
| `accent` | Buttons, links, active tabs, badges, charts |
| `accentHover` | Hover state for accent elements |
| `navBg` | Nav bar background (gradient start) |
| `navBg2` | Nav bar gradient end |
| `navText` | Nav bar text and links |
| `navTextMuted` | Inactive nav links, stats |
| `background` | Main page background |
| `text` | Primary text color |
| `textMuted` | Labels, timestamps, secondary text |
| `statusGreen` | Healthy/online indicators |
| `statusYellow` | Warning/degraded indicators |
| `statusRed` | Error/offline indicators |
| `border` | Dividers, table borders |
| `surface1` | Card backgrounds |
| `surface2` | Nested panels |
| `cardBg` | Detail panels, modals |
| `contentBg` | Content area behind cards |
| `detailBg` | Side panels, packet detail |
| `inputBg` | Text inputs, dropdowns |
| `rowStripe` | Alternating table rows |
| `rowHover` | Table row hover |
| `selectedBg` | Selected/active rows |
| `font` | Body font stack |
| `mono` | Monospace font (hex, hashes, code) |

### Node Role Colors
```json
{
  "nodeColors": {
    "repeater": "#dc2626",
    "companion": "#2563eb",
    "room": "#16a34a",
    "sensor": "#d97706",
    "observer": "#8b5cf6"
  }
}
```

Affects map markers, packet path badges, node lists, and legends.

### Packet Type Colors
```json
{
  "typeColors": {
    "ADVERT": "#22c55e",
    "GRP_TXT": "#3b82f6",
    "TXT_MSG": "#f59e0b",
    "ACK": "#6b7280",
    "REQUEST": "#a855f7",
    "RESPONSE": "#06b6d4",
    "TRACE": "#ec4899",
    "PATH": "#14b8a6",
    "ANON_REQ": "#f43f5e"
  }
}
```

Affects packet badges, feed dots, map markers, and chart colors.

### Home Page Content
```json
{
  "home": {
    "heroTitle": "Welcome to Bay Area Mesh",
    "heroSubtitle": "Find your nodes to start monitoring them.",
    "steps": [
      { "emoji": "📡", "title": "Connect", "description": "Link your node to the mesh" },
      { "emoji": "🔍", "title": "Monitor", "description": "Watch packets flow in real-time" }
    ],
    "checklist": [
      { "question": "How do I add my node?", "answer": "Search by name or paste your public key." }
    ],
    "footerLinks": [
      { "label": "📦 Packets", "url": "#/packets" },
      { "label": "🗺️ Map", "url": "#/map" }
    ]
  }
}
```

Step descriptions and checklist answers support Markdown (`**bold**`, `*italic*`, `` `code` ``, `[links](url)`).

## User vs Admin Themes

- **Admin theme** (`theme.json`): Default for all users. Edit the file, refresh.
- **User theme** (browser): Each user can override the admin theme via Tools → Customize → "Save as my theme". Stored in localStorage, only affects that browser.

User themes take priority over admin themes. Users can reset their personal theme to go back to the admin default.

## Full Example

```json
{
  "branding": {
    "siteName": "Bay Area MeshCore",
    "tagline": "Community mesh monitoring for the Bay Area",
    "logoUrl": "https://example.com/logo.svg"
  },
  "theme": {
    "accent": "#2563eb",
    "statusGreen": "#16a34a",
    "statusYellow": "#ca8a04",
    "statusRed": "#dc2626"
  },
  "themeDark": {
    "accent": "#60a5fa",
    "navBg": "#0a0a1a",
    "background": "#111827"
  },
  "nodeColors": {
    "repeater": "#ef4444",
    "observer": "#a855f7"
  },
  "home": {
    "heroTitle": "Bay Area MeshCore",
    "heroSubtitle": "Real-time monitoring for our community mesh network.",
    "steps": [
      { "emoji": "💬", "title": "Join our Discord", "description": "Get help and connect with local operators." },
      { "emoji": "📡", "title": "Advertise your node", "description": "Send an ADVERT so the network can see you." },
      { "emoji": "🗺️", "title": "Check the map", "description": "Find repeaters near you." }
    ]
  }
}
```
