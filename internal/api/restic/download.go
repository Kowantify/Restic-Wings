package restic

import (
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
    tarFile := filepath.Join(tempDir, serverId+"-"+shortId+".tar.gz")
    // Clean up any leftover file from previous failed downloads
    _ = os.Remove(tarFile)
    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    dumpCmd := exec.Command("restic", "-r", repo, "dump", "--archive", backupId, "/")
    gzipCmd := exec.Command("gzip")
    dumpCmd.Env = env
    dumpCmd.Stdout, _ = gzipCmd.StdinPipe()
    outFile, err := os.OpenFile(tarFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tar file", "details": err.Error()})
        return
    }
    defer outFile.Close()
    gzipCmd.Stdout = outFile
    if err := dumpCmd.Start(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start restic dump", "details": err.Error()})
        return
    }
    if err := gzipCmd.Start(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start gzip", "details": err.Error()})
        return
    }
    if err := dumpCmd.Wait(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic dump failed", "details": err.Error()})
        return
    }
    if err := gzipCmd.Wait(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "gzip failed", "details": err.Error()})
        return
    }
    f, err := os.Open(tarFile)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open tar file", "details": err.Error()})
        return
    }
    defer f.Close()
    if st, err := f.Stat(); err == nil {
        c.Header("Content-Length", fmt.Sprintf("%d", st.Size()))
    }
    c.Header("Content-Type", "application/gzip")
    c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=backup-%s.tar.gz", shortId))
    c.Header("X-Accel-Buffering", "no")
    c.Status(200)
    io.Copy(c.Writer, f)
}
