package restic

import (
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"

    "github.com/gin-gonic/gin"
    "github.com/pterodactyl/wings/server"
)

// GET /api/servers/:server/backups/restic/:backupId/download
func DownloadServerResticBackup(c *gin.Context) {
    serverId := c.Param("server")
    backupId := c.Param("backupId")
    encryptionKey := c.Query("encryption_key")
    ownerUsername := c.Query("owner_username")
    if serverId == "" || backupId == "" || encryptionKey == "" || ownerUsername == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing required parameters"})
        return
    }

    s := c.MustGet("server").(*server.Server)
    if err := PrepareServerResticBackup(c, s, backupId, encryptionKey, ownerUsername); err != nil {
        return
    }
    StreamPreparedResticBackup(c, s, backupId)
}

// DownloadServerResticBackupFromToken streams a Restic backup as tar.gz.
// Assumes the archive has already been prepared using PrepareServerResticBackup.
func DownloadServerResticBackupFromToken(c *gin.Context, s *server.Server, backupId string) {
    StreamPreparedResticBackup(c, s, backupId)
}

// StreamPreparedResticBackup streams a prepared Restic archive from temp storage.
func StreamPreparedResticBackup(c *gin.Context, s *server.Server, backupId string) {
    serverId := s.ID()
    if backupId == "" {
        backupId = c.Query("backup_id")
    }
    if backupId == "" {
        backupId = c.Query("backupId")
    }
    if backupId == "" {
        backupId = c.Query("id")
    }
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup_id"})
        return
    }
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp dir"})
        return
    }
    shortId := backupId
    if len(shortId) > 8 {
        shortId = shortId[:8]
    }
    gzFile := filepath.Join(tempDir, serverId+"-"+shortId+".tar.gz")
    f, err := os.Open(gzFile)
    if err != nil {
        _ = os.Remove(gzFile)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open tar file"})
        return
    }
    defer func() {
        f.Close()
        _ = os.Remove(gzFile)
    }()
    if st, err := f.Stat(); err == nil {
        if st.Size() == 0 {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "backup archive is empty"})
            return
        }
        c.Header("Content-Length", fmt.Sprintf("%d", st.Size()))
    }
    c.Header("Content-Type", "application/gzip")
    c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=backup-%s.tar.gz", shortId))
    c.Header("X-Accel-Buffering", "no")
    c.Status(200)
    io.Copy(c.Writer, f)
}
