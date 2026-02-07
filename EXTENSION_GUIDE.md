# Restic Backups Extension — Full Guide

## Overview
This extension adds Restic-based backups to Pterodactyl. It provides:
- Client UI for listing, creating, restoring, downloading, locking, and scheduling Restic snapshots.
- Admin UI for managing Restic keys.
- Panel-side API endpoints that securely proxy to Wings.
- Wings endpoints that execute Restic commands.
- Optional schedule runner (Blueprint console command + cron).

Everything lives under this extension folder and integrates with Pterodactyl’s auth and permissions.

---

## High‑Level Architecture

**Client (Panel UI)**
- Component: [dev/components/ResticBackupsTab.tsx](dev/components/ResticBackupsTab.tsx)
- Injected into the Server > Backups page via: [dev/components/Components.yml](dev/components/Components.yml)
- Uses panel session auth + CSRF headers to call the extension API

**Extension API (Panel backend)**
- Routes: [dev/web.php](dev/web.php)
- Controller: [dev/app/Http/Controllers/Admin/Extensions/resticbackups/resticbackupsExtensionController.php](dev/app/Http/Controllers/Admin/Extensions/resticbackups/resticbackupsExtensionController.php)
- Permissions enforced:
  - Owner or root admin: always allowed
  - Subusers: must have the standard Pterodactyl Backup permissions (`backup.read/create/download/restore/delete`) for the corresponding action
- Proxies to Wings using node daemon token + server encryption key

**Wings (Daemon)**
- Routes: [Restic-Wings-develop/Restic-Wings-develop/router/router.go](Restic-Wings-develop/Restic-Wings-develop/router/router.go)
- Handlers: [Restic-Wings-develop/Restic-Wings-develop/internal/api/restic/backups.go](Restic-Wings-develop/Restic-Wings-develop/internal/api/restic/backups.go)
- Executes Restic CLI commands on the node

---

## Data Storage

**Table: restic**
- Contains per‑server `encryption_key`.
- Key is generated on first install and can be regenerated in admin UI.

**Table: restic_policies**
- Stores per‑server schedule and pruning settings, plus last run timestamps.
- Used by the schedule runner script.

**Legacy tables (optional)**
- Older installs may have `restic_schedules` and/or `restic_pruning`. Current code prefers `restic_policies` and will only read legacy tables as a fallback.

---

## Client Area (Server Backups Page)

### UI Elements
- **Create Restic Backup** (primary button) — creates a new Restic snapshot.
- **Tabs**: Backups | Schedule
- **Backups list** — each snapshot row shows:
  - Timestamp
  - Size
  - ID
  - Locked indicator (lock icon)
  - Actions menu: Lock/Unlock, Download, Restore
- **Stats**
  - Total size (compressed/encrypted on disk)
  - Total uncompressed size
  - Compression/Dedup ratio
- **Hide Pterodactyl backup system** toggle (bottom)
  - Default: hidden
  - Can show the built‑in backup UI when needed

### Client‑side Behavior
- Uses `fetch` with CSRF headers for all POST requests.
- Uses pagination (`limit=5`, cursor) for backup listing.
- Uses Restic stats endpoint to calculate size metrics.
- Lock/Unlock updates UI immediately and then refreshes list.
- No full page reloads (all buttons are `type="button"`).

---

## Admin Area

**Admin view**
- File: [dev/admin/view.blade.php](dev/admin/view.blade.php)
- Controller: `resticbackupsExtensionController::index()`
- Displays per‑server encryption keys (by design).
- Allows regenerating encryption keys.
- Includes an **Archived Repos** browser (Extra Settings tab) for repos moved to `/var/lib/pterodactyl/restic/archive/` when a server is deleted on Wings.

---

## Panel API Endpoints (Extension)

All routes are under `/extensions/resticbackups` and require auth.

### Backups
- **GET** `/servers/{server}/backups/restic`
  - Lists snapshots (paginated)
  - Includes lock status (`locked`) and tags when present

- **POST** `/servers/{server}/backups/restic`
  - Creates a snapshot
  - Sends `max_backups` so Wings can prune

- **POST** `/servers/{server}/backups/restic/{backupId}/restore`
  - Restores snapshot

- **GET** `/servers/{server}/backups/restic/{backupId}/download`
  - Downloads snapshot

- **POST** `/servers/{server}/backups/restic/{backupId}/lock`
  - Tags snapshot as `locked`

- **POST** `/servers/{server}/backups/restic/{backupId}/unlock`
  - Removes `locked` tag

### Stats
- **GET** `/servers/{server}/backups/restic/stats`
  - Returns:
    - `total_size` (compressed/encrypted on disk)
    - `total_uncompressed_size` (restore size)
    - `snapshots_count`

### Schedule
- **GET** `/servers/{server}/backups/restic/schedule`
  - Returns schedule settings and next/last run

- **POST** `/servers/{server}/backups/restic/schedule`
  - Saves schedule
  - Does not run a backup immediately
  - Next run is computed from save time

---

## Wings API Endpoints

Mounted under `/api/servers/:server`:

- **POST** `/backups/restic`
  - Creates a snapshot
  - If max limit is reached, prunes oldest *unlocked* snapshots

- **GET** `/backups/restic`
  - Lists snapshots
  - Injects lock tags for any snapshot tagged `locked`
  - Adds `locked: true|false` to each snapshot

- **GET** `/backups/restic/stats`
  - Runs:
    - `restic stats --json --mode raw-data` → compressed size
    - `restic stats --json --mode restore-size` → uncompressed size
  - Returns only the fields used by the panel

- **POST** `/backups/restic/{backupId}/lock`
  - Runs: `restic tag --add locked {id}`
  - Returns `{ locked: true }`

- **POST** `/backups/restic/{backupId}/unlock`
  - Runs: `restic tag --remove locked {id}`
  - Returns `{ locked: false }`

---

## Scheduling (Automated Backups)

### Console Command
- Config: [dev/console/Console.yml](dev/console/Console.yml)
- Script: [dev/console/restic_schedule_runner.php](dev/console/restic_schedule_runner.php)
- Runs every minute via Blueprint console scheduling.

### Runner Behavior
- Looks up `restic_policies` table (legacy `restic_schedules` fallback if present)
- For each enabled schedule:
  - Computes next due time from last-run timestamp or `updated_at`
  - Runs `createBackup()` if due
  - Updates last-run timestamp
- Uses a per‑server file lock to prevent double runs

---

## Locking and Pruning Logic

**Locking**
- “Lock” is implemented by adding the Restic tag `locked` to a snapshot.
- Lock is persisted in the repo (not just UI).

**Pruning**
- When max limit is reached, Wings prunes the oldest **unlocked** snapshots.
- Locked snapshots are skipped. If all are locked, the backup request is rejected.

---

## Security & Hardening Summary

- TLS verification is enabled for daemon requests.
- No debug logging remains in client or server paths.
- CSRF is enforced for all POST routes; client sends CSRF headers.
- Daemon token and encryption key are never exposed to the client.
- Admin encryption key visibility is intentional and restricted to admin view.

---

## Operational Notes

1) Rebuild/restart Wings after changing daemon endpoints.
2) Reinstall the extension after panel changes.
3) If using self‑signed certs, configure a trusted CA for Guzzle.

---

## File Index

Client UI:
- [dev/components/ResticBackupsTab.tsx](dev/components/ResticBackupsTab.tsx)
- [dev/components/Components.yml](dev/components/Components.yml)

Panel API + Admin:
- [dev/web.php](dev/web.php)
- [dev/app/Http/Controllers/Admin/Extensions/resticbackups/resticbackupsExtensionController.php](dev/app/Http/Controllers/Admin/Extensions/resticbackups/resticbackupsExtensionController.php)
- [dev/admin/view.blade.php](dev/admin/view.blade.php)

Schedules:
- [dev/database/migrations/2026_02_01_000001_create_restic_policies_table.php](dev/database/migrations/2026_02_01_000001_create_restic_policies_table.php)
- [dev/console/Console.yml](dev/console/Console.yml)
- [dev/console/restic_schedule_runner.php](dev/console/restic_schedule_runner.php)

Wings:
- [Restic-Wings-develop/Restic-Wings-develop/router/router.go](Restic-Wings-develop/Restic-Wings-develop/router/router.go)
- [Restic-Wings-develop/Restic-Wings-develop/internal/api/restic/backups.go](Restic-Wings-develop/Restic-Wings-develop/internal/api/restic/backups.go)

---

## End‑to‑End Flow (Create Backup)

1) User clicks **Create Restic Backup** in the panel.
2) Panel sends POST to `/extensions/resticbackups/servers/{uuid}/backups/restic` with CSRF.
3) Controller verifies permissions, fetches node daemon token and encryption key.
4) Controller proxies to Wings `/api/servers/{uuid}/backups/restic` with auth + key.
5) Wings prunes oldest unlocked snapshot if needed, then runs `restic backup`.
6) Panel refreshes list and stats.

---

## End‑to‑End Flow (Lock Snapshot)

1) User selects **Lock** from snapshot menu.
2) Panel POSTs `/extensions/resticbackups/servers/{uuid}/backups/restic/{id}/lock`.
3) Controller proxies to Wings.
4) Wings tags snapshot: `restic tag --add locked {id}`.
5) UI updates to show lock icon and “Unlock” option.

---

## End‑to‑End Flow (Schedule)

1) User enables schedule and saves settings.
2) Panel saves interval in `restic_policies` (legacy `restic_schedules` fallback if present).
3) Console runner checks due schedules every minute.
4) When due, it triggers a backup via controller.
5) The schedule last-run timestamp is updated.

---

## What to Monitor in Production

- Wings logs for restic errors.
- Panel logs for auth/permission errors.
- Disk usage in `/var/lib/pterodactyl/restic`.
- Schedule runner cron/Blueprint console execution.

---

## Notes

- Locking uses Restic tags; there is no built‑in “lock” flag in Restic.
- The compression/dedup ratio is `total_uncompressed_size / total_size`.
- Pagination is per‑server; performance scales with backups per server, not total servers.
