<?php

use Illuminate\Support\Facades\Route;
use Pterodactyl\Http\Controllers\Admin\Extensions\resticbackups\resticbackupsExtensionController;

Route::middleware(['auth'])->group(function () {
    // GET backups (forward to node) - now outside /api/client and as controller method for uniformity
    Route::get('/servers/{server}/backups/restic', [resticbackupsExtensionController::class, 'listBackups']);

    // Get restic repo stats (forward to node)
    Route::get('/servers/{server}/backups/restic/stats', [resticbackupsExtensionController::class, 'getResticStats']);

    // Get restic backup status (forward to node)
    Route::get('/servers/{server}/backups/restic/status', [resticbackupsExtensionController::class, 'getResticStatus']);

    // Check if restic repo exists (forward to node)
    Route::get('/servers/{server}/backups/restic/repo/exists', [resticbackupsExtensionController::class, 'getResticRepoExists']);

    // Get restic repo disk usage (forward to node)
    Route::get('/servers/{server}/backups/restic/repo/size', [resticbackupsExtensionController::class, 'getResticRepoDiskUsage']);

    // Run restic repo health check (forward to node)
    Route::post('/servers/{server}/backups/restic/repo/check', [resticbackupsExtensionController::class, 'runResticRepoHealthCheck']);
    // Restic repo health check status (async)
    Route::get('/servers/{server}/backups/restic/repo/check/status', [resticbackupsExtensionController::class, 'getResticRepoHealthStatus']);

    // Download a specific backup
    Route::get('/servers/{server}/backups/restic/{backupId}/download', [resticbackupsExtensionController::class, 'downloadBackup']);
    Route::get('/servers/{server}/backups/restic/{backupId}/download/stream', [resticbackupsExtensionController::class, 'downloadBackupStream']);
    Route::post('/servers/{server}/backups/restic/{backupId}/download/prepare', [resticbackupsExtensionController::class, 'downloadBackupPrepare']);
    Route::get('/servers/{server}/backups/restic/{backupId}/download/status', [resticbackupsExtensionController::class, 'downloadBackupStatus']);
    Route::get('/servers/{server}/backups/restic/{backupId}/download/url', [resticbackupsExtensionController::class, 'downloadBackupUrl']);

    // Restore a specific backup
    Route::post('/servers/{server}/backups/restic/{backupId}/restore', [resticbackupsExtensionController::class, 'restoreBackup']);
    // Restore status (async)
    Route::get('/servers/{server}/backups/restic/restore/status', [resticbackupsExtensionController::class, 'getResticRestoreStatus']);
    // Delete a specific backup
    Route::delete('/servers/{server}/backups/restic/{backupId}', [resticbackupsExtensionController::class, 'deleteBackup']);

    // Create a backup
    Route::post('/servers/{server}/backups/restic', [resticbackupsExtensionController::class, 'createBackup']);

    // Restic schedule settings
    Route::get('/servers/{server}/backups/restic/schedule', [resticbackupsExtensionController::class, 'getResticSchedule']);
    Route::post('/servers/{server}/backups/restic/schedule', [resticbackupsExtensionController::class, 'saveResticSchedule']);

    // Restic settings
    Route::get('/settings', [resticbackupsExtensionController::class, 'getResticSettings']);
    Route::get('/servers/{server}/backups/restic/limits', [resticbackupsExtensionController::class, 'getResticLimits']);

    // Restic pruning settings
    Route::get('/servers/{server}/backups/restic/pruning', [resticbackupsExtensionController::class, 'getResticPruning']);
    Route::post('/servers/{server}/backups/restic/pruning', [resticbackupsExtensionController::class, 'saveResticPruning']);
    Route::post('/servers/{server}/backups/restic/pruning/run', [resticbackupsExtensionController::class, 'runResticPrune']);
    // Restic prune status (async)
    Route::get('/servers/{server}/backups/restic/pruning/status', [resticbackupsExtensionController::class, 'getResticPruneStatus']);

    // Lock/unlock a backup to prevent pruning
    Route::post('/servers/{server}/backups/restic/{backupId}/lock', [resticbackupsExtensionController::class, 'lockBackup']);
    Route::post('/servers/{server}/backups/restic/{backupId}/unlock', [resticbackupsExtensionController::class, 'unlockBackup']);
    Route::get('/servers/{server}/backups/restic/locks', [resticbackupsExtensionController::class, 'getResticLocks']);
    Route::post('/servers/{server}/backups/restic/unlock', [resticbackupsExtensionController::class, 'unlockResticRepo']);


    // Backup notes
    Route::post('/servers/{server}/backups/restic/{backupId}/note', [resticbackupsExtensionController::class, 'saveBackupNote']);

    // Admin-only: download node patch installer. The controller enforces root_admin.
    Route::post('/download-script', [resticbackupsExtensionController::class, 'downloadScript'])->name('admin.extensions.resticbackups.downloadScript');

    // Admin-only helpers (controller enforces root_admin). Used by the admin blade so we don't embed
    // all secrets into the page source.
    Route::get('/admin/encryption-key', [resticbackupsExtensionController::class, 'adminGetEncryptionKey']);
    Route::get('/admin/key-history', [resticbackupsExtensionController::class, 'adminGetKeyHistory']);

    // Admin-only: browse/download/delete archived Restic repos on nodes (server deletion archives).
    Route::get('/admin/archived-repos', [resticbackupsExtensionController::class, 'adminListArchivedRepos']);
    Route::get('/admin/archived-repos/{nodeId}/{archiveId}/download', [resticbackupsExtensionController::class, 'adminDownloadArchivedRepo']);
    Route::delete('/admin/archived-repos/{nodeId}/{archiveId}', [resticbackupsExtensionController::class, 'adminDeleteArchivedRepo']);
});
