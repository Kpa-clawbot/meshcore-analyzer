#!/usr/bin/env python3
"""
Delete nodes from the database that fall outside the configured geo_filter polygon + bufferKm.
Nodes with no GPS coordinates are always kept.

Usage:
  python3 prune-nodes-outside-geo-filter.py [db_path] [--dry-run]

  db_path   Path to meshcore.db  (default: /app/data/meshcore.db)
  --dry-run  Show what would be deleted without making any changes
"""

import sqlite3
import math
import sys
import json
import os

# ---------------------------------------------------------------------------
# geo_filter config — paste your polygon here (or let the script read
# config.json automatically when run inside the container)
# ---------------------------------------------------------------------------
POLYGON = [
    [51.087294, 2.543335],
    [50.841814, 2.614746],
    [50.692512, 2.911377],
    [50.775677, 3.147583],
    [50.524993, 3.279419],
    [50.476093, 3.630981],
    [50.315067, 3.685913],
    [50.265951, 4.141846],
    [49.984311, 4.11438],
    [49.49815,  5.465698],
    [49.544491, 5.83374],
    [50.329091, 6.410522],
    [50.754837, 6.053467],
    [51.15953,  5.844727],
    [51.300512, 5.509644],
    [51.485537, 5.042725],
    [51.482117, 4.520874],
    [51.375983, 3.378296],
]
BUFFER_KM = 20.0
# ---------------------------------------------------------------------------


def point_in_polygon(lat, lon, polygon):
    """Ray-casting algorithm."""
    inside = False
    n = len(polygon)
    j = n - 1
    for i in range(n):
        yi, xi = polygon[i]   # lat, lon
        yj, xj = polygon[j]
        if ((yi > lat) != (yj > lat)) and (lon < (xj - xi) * (lat - yi) / (yj - yi) + xi):
            inside = not inside
        j = i
    return inside


def dist_to_segment_km(lat, lon, a, b):
    """Approximate distance (km) from point to line segment, using flat-earth projection."""
    lat1, lon1 = a
    lat2, lon2 = b
    mid_lat = (lat1 + lat2) / 2.0
    cos_lat = math.cos(math.radians(mid_lat))
    km_per_deg_lat = 111.0
    km_per_deg_lon = 111.0 * cos_lat

    # Translate so point is at origin
    ax = (lon1 - lon) * km_per_deg_lon
    ay = (lat1 - lat) * km_per_deg_lat
    bx = (lon2 - lon) * km_per_deg_lon
    by = (lat2 - lat) * km_per_deg_lat

    abx, aby = bx - ax, by - ay
    ab_sq = abx * abx + aby * aby
    if ab_sq == 0:
        return math.sqrt(ax * ax + ay * ay)

    t = max(0.0, min(1.0, -(ax * abx + ay * aby) / ab_sq))
    px = ax + t * abx
    py = ay + t * aby
    return math.sqrt(px * px + py * py)


def node_passes_filter(lat, lon, polygon, buffer_km):
    """Return True if the node should be kept."""
    if lat is None or lon is None:
        return True
    if lat == 0.0 and lon == 0.0:
        return True  # no GPS fix
    if point_in_polygon(lat, lon, polygon):
        return True
    if buffer_km > 0:
        n = len(polygon)
        for i in range(n):
            j = (i + 1) % n
            if dist_to_segment_km(lat, lon, polygon[i], polygon[j]) <= buffer_km:
                return True
    return False


def main():
    args = sys.argv[1:]
    dry_run = '--dry-run' in args
    args = [a for a in args if not a.startswith('--')]

    db_path = args[0] if args else '/app/data/meshcore.db'

    if not os.path.exists(db_path):
        print(f"ERROR: database not found at {db_path}")
        sys.exit(1)

    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()

    cur.execute('SELECT public_key, name, lat, lon FROM nodes ORDER BY name')
    nodes = cur.fetchall()

    keep, remove = [], []
    for row in nodes:
        lat = row['lat']
        lon = row['lon']
        if node_passes_filter(lat, lon, POLYGON, BUFFER_KM):
            keep.append(row)
        else:
            remove.append(row)

    print(f"Total nodes in DB : {len(nodes)}")
    print(f"Nodes to keep     : {len(keep)}")
    print(f"Nodes to delete   : {len(remove)}")

    if not remove:
        print("\nNothing to delete.")
        conn.close()
        return

    print("\nNodes that will be DELETED:")
    for row in remove:
        lat = row['lat'] or 0
        lon = row['lon'] or 0
        name = row['name'] or row['public_key'][:12]
        print(f"  {name:<30}  lat={lat:.4f}  lon={lon:.4f}")

    if dry_run:
        print("\n[dry-run] No changes made.")
        conn.close()
        return

    confirm = input(f"\nDelete {len(remove)} nodes? Type 'yes' to confirm: ").strip()
    if confirm.lower() != 'yes':
        print("Aborted.")
        conn.close()
        return

    pubkeys = [row['public_key'] for row in remove]
    cur.executemany('DELETE FROM nodes WHERE public_key = ?', [(pk,) for pk in pubkeys])
    conn.commit()
    print(f"\nDeleted {cur.rowcount if cur.rowcount >= 0 else len(pubkeys)} nodes.")
    conn.close()


if __name__ == '__main__':
    main()
