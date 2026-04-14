# Geographic Filtering

CoreScope supports geographic filtering to restrict which nodes are ingested and returned in API responses. This is useful for public-facing deployments that should only show activity in a specific region.

## How it works

Geographic filtering operates at two levels:

- **Ingest time** — ADVERT packets carrying GPS coordinates are rejected by the ingestor if the node falls outside the configured area. The node never reaches the database.
- **API responses** — Nodes already in the database are filtered from the `/api/nodes` response if they fall outside the area. This covers nodes ingested before the filter was configured.

Nodes with no GPS fix (`lat=0, lon=0` or missing coordinates) always pass the filter regardless of configuration.

## Configuration

Add a `geo_filter` block to `config.json`:

```json
"geo_filter": {
  "polygon": [
    [51.55, 3.80],
    [51.55, 5.90],
    [50.65, 5.90],
    [50.65, 3.80]
  ],
  "bufferKm": 20
}
```

| Field | Type | Description |
|-------|------|-------------|
| `polygon` | `[[lat, lon], ...]` | Array of at least 3 coordinate pairs defining the boundary |
| `bufferKm` | number | Extra distance (km) outside the polygon edge that is also accepted. `0` = exact boundary |

Both the server and the ingestor read `geo_filter` from `config.json`. Restart both after changing this section manually.

To disable filtering entirely, remove the `geo_filter` block.

### Legacy bounding box

An older bounding box format is also supported as a fallback when no `polygon` is present:

```json
"geo_filter": {
  "latMin": 50.65,
  "latMax": 51.55,
  "lonMin": 3.80,
  "lonMax": 5.90
}
```

Prefer the polygon format — it supports irregular shapes and the `bufferKm` margin.

## Configuring via the customizer

If your server has an `apiKey` configured, the **GeoFilter tab** in the Customizer lets you edit the polygon visually without touching `config.json`:

1. Open the Customizer (nav bar → customize icon)
2. Click the **🗺️ GeoFilter** tab
3. Click on the map to draw your polygon (at least 3 points)
4. Adjust **Buffer km**
5. Enter your **Server API Key** (the `apiKey` value from `config.json`)
6. Click **Save to server** — the filter is applied immediately, no restart needed

The editing controls only appear when the server has a write-capable API key configured. On deployments without an `apiKey`, the tab shows the current polygon as read-only.

To remove the filter, click **Remove filter** (also requires the API key).

## GeoFilter Builder (standalone tool)

For a full-screen editing experience, use the built-in GeoFilter Builder at `/geofilter-builder.html`:

1. Navigate to `http://your-server/geofilter-builder.html`
2. Click on the map to add polygon vertices
3. Adjust **Buffer km** (default 20)
4. Copy the generated JSON from the output panel
5. Paste it as a top-level key into `config.json` and restart the server

The builder is also accessible from the Customizer's Export tab via the **GeoFilter Builder →** link.

For local/offline use without a running server, open `tools/geofilter-builder.html` directly in a browser.

## API endpoint

```
GET /api/config/geo-filter
```

Returns the current geo filter configuration. Also includes a `writeEnabled` boolean indicating whether the `PUT` endpoint is available (i.e., server has a write-capable `apiKey`).

```
PUT /api/config/geo-filter
```

Requires `X-API-Key` header. Saves the polygon to `config.json` and applies it in-memory immediately.

Request body:
```json
{"polygon": [[lat, lon], ...], "bufferKm": 20}
```

To clear the filter, send `{"polygon": null}`.

```
POST /api/admin/prune-geo-filter
POST /api/admin/prune-geo-filter?confirm=true
```

Requires `X-API-Key` header. Without `?confirm=true`, performs a dry run and returns the list of nodes that would be deleted. With `?confirm=true`, permanently deletes them from the database.

Response (dry run or confirmed):
```json
{"deleted": 5, "nodes": [{"pubKey": "...", "name": "NodeName", "lat": 51.12, "lon": 4.50}]}
```

## Cleaning up historical nodes

The ingestor prevents new out-of-bounds nodes from being ingested, but it does not retroactively remove nodes stored before the filter was configured.

### One-click prune from the Customizer (recommended)

If `writeEnabled` is true (server has a write-capable `apiKey`), the GeoFilter tab shows a **Prune nodes** section at the bottom:

1. Click **Preview** — the server dry-runs the deletion and lists every node that falls outside the current polygon + buffer. No data is deleted yet.
2. Review the list. It shows the node name (or public key) and coordinates.
3. Click **Confirm delete** to permanently remove those nodes from the database.

Nodes without GPS coordinates are always kept.

### CLI alternative (Python script)

**File:** `scripts/prune-nodes-outside-geo-filter.py`

```bash
# Dry run — shows what would be deleted without making changes
python3 scripts/prune-nodes-outside-geo-filter.py --dry-run

# Default paths: /app/data/meshcore.db and /app/config.json
python3 scripts/prune-nodes-outside-geo-filter.py

# Custom paths
python3 scripts/prune-nodes-outside-geo-filter.py /path/to/meshcore.db \
  --config /path/to/config.json

# In Docker
docker exec -it meshcore-analyzer \
  python3 /app/scripts/prune-nodes-outside-geo-filter.py --dry-run
```

The script reads `geo_filter.polygon` and `geo_filter.bufferKm` from config, lists nodes that fall outside, then asks for `yes` confirmation before deleting. Nodes without coordinates are always kept.

Both the UI button and the script are **one-time migration tools** — run once after first configuring `geo_filter` to clean up pre-filter data. The ingestor handles all subsequent filtering automatically.
