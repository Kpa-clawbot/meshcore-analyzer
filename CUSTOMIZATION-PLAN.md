# CUSTOMIZATION-PLAN.md — White-Label / Multi-Instance Theming

## Problem

Regional mesh admins (e.g. CascadiaMesh) fork the analyzer and manually edit CSS/HTML to customize branding, colors, and content. This is fragile — every upstream update requires re-applying customizations.

## Goal

A `config.json`-driven customization system where admins configure branding, colors, labels, and home page content without touching source code. Accessible via a **Tools → Customization** UI that outputs the config.

## Direct Feedback (CascadiaMesh Admin)

Customizations they made manually:
- **Branding**: Custom logo, favicon, site title ("CascadiaMesh Analyzer")
- **Colors**: Node type colors (repeaters blue instead of red, companions red)
- **UI styling**: Custom color scheme (deep navy theme — "Cascadia" theme)
- **Home page**: Intro section emojis, steps, checklist content

Requested config options:
- Configurable branding assets (logo, favicon, site name)
- Configurable UI colors/text labels
- Configurable node type colors
- Everything in the intro/home section should be configurable

## Config Schema (proposed)

```json
{
  "branding": {
    "siteName": "CascadiaMesh Analyzer",
    "logoUrl": "/assets/logo.png",
    "faviconUrl": "/assets/favicon.ico",
    "tagline": "Pacific Northwest Mesh Network Monitor"
  },
  "theme": {
    "accent": "#20468b",
    "accentHover": "#2d5bb0",
    "navBg": "#111c36",
    "navBg2": "#060a13",
    "statusGreen": "#45644c",
    "statusYellow": "#b08b2d",
    "statusRed": "#b54a4a"
  },
  "nodeColors": {
    "repeater": "#3b82f6",
    "companion": "#ef4444",
    "room": "#8b5cf6",
    "sensor": "#10b981",
    "observer": "#f59e0b"
  },
  "home": {
    "heroTitle": "CascadiaMesh Network Monitor",
    "heroSubtitle": "Real-time packet analysis for the Pacific Northwest mesh",
    "steps": [
      { "emoji": "📡", "title": "Connect", "description": "Link your node to the mesh" },
      { "emoji": "🔍", "title": "Monitor", "description": "Watch packets flow in real-time" },
      { "emoji": "📊", "title": "Analyze", "description": "Understand your network's health" }
    ],
    "checklist": [
      { "question": "How do I add my node?", "answer": "..." },
      { "question": "What regions are covered?", "answer": "..." }
    ],
    "footerLinks": [
      { "label": "Discord", "url": "https://discord.gg/..." },
      { "label": "GitHub", "url": "https://github.com/..." }
    ]
  },
  "labels": {
    "latestPackets": "Latest Packets",
    "liveMap": "Live Map"
  }
}
```

## Implementation Plan

### Phase 1: Config Loading + CSS Variables (Server)
- Server reads `config.json` theme section
- New endpoint: `GET /api/config/theme` returns merged theme config
- Client injects CSS variables from theme config on page load
- Node type colors configurable via `window.TYPE_COLORS` override

### Phase 2: Branding
- Config drives nav bar title, logo, favicon
- `index.html` rendered server-side with branding placeholders OR
- Client JS replaces branding elements on load from `/api/config/theme`

### Phase 3: Home Page Content
- Home page sections (hero, steps, checklist, footer) driven by config
- Default content baked in; config overrides specific sections
- Emoji + text for each step configurable

### Phase 4: Tools → Customization UI
- New page `#/customize` (admin only?)
- Color pickers for theme variables
- Live preview
- Branding upload (logo, favicon)
- Export as JSON config
- Home page content editor (WYSIWYG-lite)

### Phase 5: CSS Theme Presets
- Built-in themes: Default (blue), Cascadia (navy), Forest (green), Midnight (dark)
- One-click theme switching
- Custom theme = override any variable

## Architecture Notes

- Theme CSS variables are already in `:root {}` — just need to override from config
- Node type colors used in `roles.js` via `TYPE_COLORS` — make configurable
- Home page content is in `home.js` — extract to template driven by config
- Logo/favicon: serve from config-specified path, default to built-in
- No build step — pure runtime configuration
- Config changes take effect on page reload (no server restart needed for theme)

## Priority

1. Theme colors (CSS variables from config) — highest impact, lowest effort
2. Branding (site name, logo) — visible, requested
3. Node type colors — requested specifically
4. Home page content — requested
5. Customization UI — nice to have, lower priority
