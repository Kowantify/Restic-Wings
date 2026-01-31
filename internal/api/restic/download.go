package restic

import (
    "bytes"
    "fmt"
    "io"
    "net/http"
    "os"
    "os/exec"
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
    DownloadServerResticBackupFromToken(c, s, backupId, encryptionKey, ownerUsername)
}

// DownloadServerResticBackupFromToken streams a Restic backup as tar.gz
func DownloadServerResticBackupFromToken(c *gin.Context, s *server.Server, backupId, encryptionKey, ownerUsername string) {
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
    // Compose repo and temp file path
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s+%s", serverId, ownerUsername)
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp dir", "details": err.Error()})
        return
    }
    // Use restic dump --archive to create a tar.gz of the backup root
    shortId := backupId
    if len(shortId) > 8 {
        shortId = shortId[:8]
    }
    tarFile := filepath.Join(tempDir, serverId+"-"+shortId+".tar")
    gzFile := tarFile + ".gz"
    // Clean up any leftover file from previous failed downloads
    _ = os.Remove(tarFile)
    _ = os.Remove(gzFile)

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    restoreDir := filepath.Join(tempDir, serverId+"-"+shortId+"-restore")
    _ = os.RemoveAll(restoreDir)

    restoreCmd := exec.Command("restic", "-r", repo, "restore", backupId, "--target", restoreDir)
    restoreCmd.Env = env

    var restoreErr bytes.Buffer
    restoreCmd.Stderr = &restoreErr
    if err := restoreCmd.Run(); err != nil {
        _ = os.RemoveAll(restoreDir)
        details := restoreErr.String()
        if details == "" {
            details = err.Error()
        }
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic restore failed", "details": details})
        return
    }

    volumeSubdir := filepath.Join(restoreDir, "var/lib/pterodactyl/volumes", serverId)
    tarBase := restoreDir
    if st, err := os.Stat(volumeSubdir); err == nil && st.IsDir() {
        tarBase = volumeSubdir
    }
    tarCmd := exec.Command("tar", "-czf", gzFile, "-C", tarBase, ".")
    var tarErr bytes.Buffer
    tarCmd.Stderr = &tarErr
    if err := tarCmd.Run(); err != nil {
        details := tarErr.String()
        if details == "" {
            details = err.Error()
        }
        c.JSON(http.StatusInternalServerError, gin.H{"error": "tar failed", "details": details})
        return
    }
    _ = os.RemoveAll(restoreDir)

    f, err := os.Open(gzFile)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open tar file", "details": err.Error()})
        return
    }
    defer f.Close()
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
