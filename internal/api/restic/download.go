package restic

import (
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "time"

    "github.com/gin-gonic/gin"
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

    // Compose repo and temp file path
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s+%s", serverId, ownerUsername)
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp dir", "details": err.Error()})
        return
    }
    // Use restic dump to create a tar.gz of the backup root
    tarFile := filepath.Join(tempDir, serverId+"-"+backupId+".tar.gz")
    // Clean up any leftover file from previous failed downloads
    os.Remove(tarFile)
    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    dumpCmd := exec.Command("restic", "-r", repo, "dump", backupId, serverId)
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
    defer func() {
        f.Close()
        os.Remove(tarFile)
    }()
    c.Header("Content-Type", "application/gzip")
    c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=backup-%s.tar.gz", backupId))
    c.Header("X-Accel-Buffering", "no")
    http.ServeContent(c.Writer, c.Request, tarFile, time.Now(), f)
}
