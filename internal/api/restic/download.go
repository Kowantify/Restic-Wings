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
    tempFile := filepath.Join(tempDir, backupId+".tar.gz")

    // Use restic to dump the backup to a tar.gz file
    // This assumes the backup is a snapshot and can be dumped as a tar archive
    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    dumpCmd := exec.Command("restic", "-r", repo, "dump", backupId, "/", "--archive")
    outFile, err := os.OpenFile(tempFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp file", "details": err.Error()})
        return
    }
    defer outFile.Close()
    dumpCmd.Env = env
    dumpCmd.Stdout = outFile
    if err := dumpCmd.Run(); err != nil {
        os.Remove(tempFile)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic dump failed", "details": err.Error()})
        return
    }

    // Stream the file to the user
    f, err := os.Open(tempFile)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open temp file", "details": err.Error()})
        return
    }
    defer func() {
        f.Close()
        os.Remove(tempFile)
    }()

    c.Header("Content-Type", "application/gzip")
    c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=backup-%s.tar.gz", backupId))
    c.Header("X-Accel-Buffering", "no")
    http.ServeContent(c.Writer, c.Request, tempFile, time.Now(), f)
}
