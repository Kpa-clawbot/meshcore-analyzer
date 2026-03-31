## Learnings — Pre-Session Archived Notes (2026-03-26 to 2026-03-27 early)

Historical context from earlier phases:

### V8 Heap Analysis (2026-03-27 06:00 UTC)
- Tested NODE_OPTIONS=4GB heap for staging with 1.17M observations
- Result: 2.7GB peak RAM (35% of 7.7GB VM), successful load completion
- Lesson: Default 1.7GB heap insufficient for large datasets; explicit NODE_OPTIONS needed

### Production Issue Diagnosis (2026-03-27 02:20 UTC)
- Root cause identified: SQLite WAL checkpoint failure causing 100% CPU
- Database entered locked state; transaction retries caused spin loop
- Solution: Restart container to recover from locked state
- Corrupted WAL file was non-recoverable (4.7MB unrecoverable log)

### Azure VM Cost Analysis (requested 2026-03-27)
- Current: Standard_D2as_v5 (2 vCPU, 8GB) ~-75/mo
- Alternative: Standard_B2s (2 vCPU, 4GB) ~-18/mo for testing
- Recommendation: Reserved instances for 30-40% savings if stable

### Staging DB Setup Planning (2026-03-27 ~04:41 UTC)
- Prod DB location: Docker volume (21MB, fresh after incident)
- Old DB: ~/meshcore-data-old/meshcore.db (185MB, problematic)
- Staging destination: ~/meshcore-staging-data/ (copy for debugging)
- Key insight: Docker Compose migration requires volume → bind mount data migration

### Docker Compose Architecture Design (2026-03-27, Issue #132 M1)
- Created docker-compose.yml with prod + staging services
- Prod: ports 80/443/1883; Staging: ports 81/1884 (HTTP only)
- Data: bind mounts (~/meshcore-data, ~/meshcore-staging-data)
- Caddy TLS: Docker volumes (prod/staging separate)
- env vars: Configurable ports, configurable data paths
- Profiles: Staging only starts with --profile staging

### manage.sh Orchestration Updates (2026-03-27, Issue #132 M2)
- Added Compose mode detection + legacy single-container fallback
- New commands: start/stop/restart/status/logs/promote
- Staging-specific: prepare_staging_db(), prepare_staging_config()
- All existing tests pass (62 packet-filter, 29 aging)

---
---

## Massive Session - 2026-03-27 (FULL DAY)

### Database Merge Execution
- **Status:** ✅ Complete, deployed to production
- **Pre-merge verification:** Disk space confirmed, schemas both v3, counts captured
- **Backup creation:** Timestamped /home/deploy/backups/pre-merge-20260327-071425/ with prod + staging DBs
- **Merge execution:** Staging DB used as base (superset). Transmissions INSERT OR IGNORE by hash. Observations all unique. Nodes/observers latest-wins + sum counts.
- **Results:** 51,723 tx + 1,237,186 obs merged. Hash uniqueness verified. Spot check passed.
- **Deployment:** Docker Compose managed meshcore-prod (replaced old Docker volume approach). Load time 8,491ms. Memory 860MiB RSS (no NODE_OPTIONS needed — RAM fix proved effective).
- **Health:** Healthy within 30s. External access via https://analyzer.00id.net ✅

### Infrastructure Changes
- deploy user SSH key + docker group re-added via Azure CLI
- Old Docker volumes removed
- NODE_OPTIONS hack removed (no longer needed post-RAM-fix)

### Docker Compose Migration
- **Volume paths unified:** caddy-data (prod), caddy-data-staging (staging)
- **Data directories:** ~/meshcore-data (prod), ~/meshcore-staging-data (staging) via bind mounts
- **Config files:** Separate config.prod.json, config.staging.json
- **Caddyfile:** Separate Caddyfile.prod (HTTPS), Caddyfile.staging (HTTP :81)

### Staging Environment Setup
- **Data:** ~/meshcore-staging-data/ with copy of problematic DB (185MB) for debugging
- **Purpose:** Debug corrupted WAL from 100% CPU incident
- **MQTT:** Port 1884 (separate from prod 1883)
- **HTTP:** Plaintext port 81 (no HTTPS)

### CI Pipeline Updates
- **Docker Compose v2 auto-check:** CI deploy job now auto-installs docker-compose-plugin if missing (self-healing per user directive)
- **Staging auto-deploy:** Build image once, deploy staging auto on every master push. Health check via Docker Compose.
- **Production manual:** No auto-restart of prod. Promotion via ./manage.sh promote (Hudson only).

### Testing & Validation
- ✅ docker-compose config validation
- ✅ Service startup verification
- ✅ Volume mount verification (data persistence)
- ✅ Health check behavior (Docker Compose native)

### Key Decisions Applied
- Only Hudson touches prod infrastructure (user directive)
- Go staging runs on port 82 (future phase)
- Backups retained 7 days post-merge
- Manual promotion flow (no auto-promotion to prod)

