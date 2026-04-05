# Customization

CoreScope includes a built-in theme customizer. Access it from **Tools → Customization** in the navigation menu.

[Screenshot: theme customizer panel with color pickers]

## What you can customize

### Branding

- **Site name** — displayed in the nav bar and browser tab
- **Tagline** — shown on the home page
- **Logo URL** — replace the default logo
- **Favicon URL** — custom browser tab icon

### Theme colors (Light & Dark)

Every color in the UI is customizable:

- **Accent** — primary color for links, buttons, highlights
- **Navigation** — nav bar background, text, and muted text colors
- **Background** — page background and content area
- **Surfaces** — cards, panels, input fields, detail panes
- **Status** — green (healthy), yellow (degraded), red (silent)
- **Text** — primary text, muted text, borders
- **Tables** — row stripe, hover, and selected row colors

Both light and dark themes are independently configurable.

### Node colors

Set the color for each role: repeater, companion, room, sensor, observer. These colors appear on the map, in node badges, and throughout the UI.

### Packet type colors

Customize the color for each packet type: Advert, Channel Msg, Direct Msg, ACK, Request, Response, Trace, Path.

### Home page

Customize the onboarding experience:

- Hero title and subtitle
- Getting-started steps (emoji, title, description for each)
- FAQ items
- Footer links

### Timestamps

- **Display mode** — relative ("5 min ago") or absolute
- **Timezone** — local or UTC
- **Format preset** — ISO or other presets

## Live preview

Changes apply instantly as you edit. You see the result in real time without saving.

## Exporting a theme

Click **Export JSON** to download your customizations as a JSON file. This produces a config-compatible block you can paste into your `config.json`.

## Importing a theme

Click **Import JSON** and paste a previously exported theme. The customizer loads all values and applies them immediately.

## Resetting

Click **Reset to Defaults** to restore all settings to the built-in defaults.

## How it works

The customizer writes CSS custom properties (variables) to override the defaults. Exported JSON maps directly to the `theme`, `nodeColors`, `branding`, and `home` sections of [config.json](configuration.md).

## Tips

- Start with the accent color — it cascades through buttons, links, and highlights
- Dark mode has its own color set (`themeDark`), independent of light mode
- Node colors affect the [Map](map.md), [Live](live.md) page, and node badges everywhere
- Export your theme before upgrading CoreScope, then re-import it after
