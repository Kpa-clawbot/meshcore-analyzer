# CoreScope Migration Guide

MeshCore Analyzer has been renamed to **CoreScope**. This document covers what you need to update.

## What Changed

- **Repository name**: `meshcore-analyzer` → `corescope`
- **Docker image name**: `meshcore-analyzer:latest` → `corescope:latest`
- **Docker container prefixes**: `meshcore-*` → `corescope-*`
- **Default site name**: "MeshCore Analyzer" → "CoreScope"

## What Did NOT Change

- **Data directories** — `~/meshcore-data/` stays as-is
- **Database filename** — `meshcore.db` is unchanged
- **MQTT topics** — `meshcore/#` topics are protocol-level and unchanged
- **Browser state** — Favorites, localStorage keys, and settings are preserved
- **Config file format** — `config.json` structure is the same

---

## 1. Git Remote Update

Update your local clone to point to the new repository URL:

```bash
git remote set-url origin https://github.com/Kpa-clawbot/corescope.git
git pull
```

## 2. Docker (manage.sh) Users

Rebuild with the new image name:

```bash
./manage.sh stop
git pull
./manage.sh setup
```

The new image is `corescope:latest`. You can clean up the old image:

```bash
docker rmi meshcore-analyzer:latest
```

## 3. Docker Compose Users

Rebuild containers with the new names:

```bash
docker compose down
git pull
docker compose build
docker compose up -d
```

Container names change from `meshcore-*` to `corescope-*`. Old containers are removed by `docker compose down`.

## 4. Data Directories

**No action required.** The data directory `~/meshcore-data/` and database file `meshcore.db` are unchanged. Your existing data carries over automatically.

## 5. Config

If you customized `branding.siteName` in your `config.json`, update it to your preferred name. Otherwise the new default "CoreScope" applies automatically.

No other config keys changed.

## 6. MQTT

**No action required.** MQTT topics (`meshcore/#`) are protocol-level and are not affected by the rename.

## 7. Browser

**No action required.** Bookmarks/favorites will continue to work at the same host and port. localStorage keys are unchanged, so your settings and preferences are preserved.

## 8. CI/CD

If you have custom CI/CD pipelines that reference:

- The old repository URL (`meshcore-analyzer`)
- The old Docker image name (`meshcore-analyzer:latest`)
- Old container names (`meshcore-*`)

Update those references to use the new names.

---

## Summary Checklist

| Item | Action Required? | What to Do |
|------|-----------------|------------|
| Git remote | ✅ Yes | `git remote set-url origin …corescope.git` |
| Docker image | ✅ Yes | Rebuild; optionally `docker rmi` old image |
| Docker Compose | ✅ Yes | `docker compose down && build && up` |
| Data directories | ❌ No | Unchanged |
| Config | ⚠️ Maybe | Only if you customized `branding.siteName` |
| MQTT | ❌ No | Topics unchanged |
| Browser | ❌ No | Settings preserved |
| CI/CD | ⚠️ Maybe | Update if referencing old repo/image names |
