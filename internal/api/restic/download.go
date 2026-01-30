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
    if serverId == "" || backupId == "" || encryptionKey == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing required parameters"})
        return
    }

    // Compose repo and temp file path
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", serverId)
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp dir", "details": err.Error()})
        return
    }
    // Use restic restore to extract the backup to tempDir/backupId/
    restoreTarget := filepath.Join(tempDir, serverId+"-"+backupId)
    if err := os.MkdirAll(restoreTarget, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create restore dir", "details": err.Error()})
        return
    }
    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    restoreCmd := exec.Command("restic", "-r", repo, "restore", backupId, "--target", restoreTarget)
    restoreCmd.Env = env
    if err := restoreCmd.Run(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic restore failed", "details": err.Error()})
        return
    }
    // Tar only the restored directory for this backup
    tarFile := filepath.Join(tempDir, serverId+"-"+backupId+".tar.gz")
    tarCmd := exec.Command("tar", "-czf", tarFile, "-C", restoreTarget, ".")
    if err := tarCmd.Run(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tar archive", "details": err.Error()})
        return
    }
    f, err := os.Open(tarFile)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open tar file", "details": err.Error()})
        return
    }
    defer func() {
        f.Close()
        os.RemoveAll(restoreTarget)
        os.Remove(tarFile)
    }()
    c.Header("Content-Type", "application/gzip")
    c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=backup-%s.tar.gz", backupId))
    c.Header("X-Accel-Buffering", "no")
    http.ServeContent(c.Writer, c.Request, tarFile, time.Now(), f)
}
