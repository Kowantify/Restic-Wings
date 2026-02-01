package restic

import (
    "bytes"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"

    "github.com/gin-gonic/gin"
    "github.com/pterodactyl/wings/server"
)

// POST /api/servers/:server/backups/restic/:backupId/prepare
func PrepareServerResticBackupHandler(c *gin.Context) {
    backupId := c.Param("backupId")
    var body struct {
        EncryptionKey string `json:"encryption_key"`
        OwnerUsername string `json:"owner_username"`
    }
    _ = c.ShouldBindJSON(&body)

    encryptionKey := body.EncryptionKey
    ownerUsername := body.OwnerUsername
    if encryptionKey == "" {
        encryptionKey = c.Query("encryption_key")
    }
    if ownerUsername == "" {
        ownerUsername = c.Query("owner_username")
    }

    s := c.MustGet("server").(*server.Server)
    if err := PrepareServerResticBackup(c, s, backupId, encryptionKey, ownerUsername); err != nil {
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "prepared"})
}

// PrepareServerResticBackup restores a snapshot to a temp directory and creates a tar.gz archive.
func PrepareServerResticBackup(c *gin.Context, s *server.Server, backupId, encryptionKey, ownerUsername string) error {
    serverId := s.ID()
    if backupId == "" {
        backupId = c.Query("backup_id")
    }
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup_id"})
        return fmt.Errorf("missing backup_id")
    }
    if encryptionKey == "" || ownerUsername == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing encryption_key or owner_username"})
        return fmt.Errorf("missing encryption_key or owner_username")
    }

    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s+%s", serverId, ownerUsername)
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp dir"})
        return err
    }

    shortId := backupId
    if len(shortId) > 8 {
        shortId = shortId[:8]
    }

    restoreDir := filepath.Join(tempDir, serverId+"-"+shortId+"-restore")
    _ = os.RemoveAll(restoreDir)

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    restoreCmd := exec.Command("restic", "-r", repo, "restore", backupId, "--target", restoreDir)
    restoreCmd.Env = env

    var restoreErr bytes.Buffer
    restoreCmd.Stderr = &restoreErr
    if err := restoreCmd.Run(); err != nil {
        _ = os.RemoveAll(restoreDir)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic restore failed"})
        return err
    }

    volumeSubdir := filepath.Join(restoreDir, "var/lib/pterodactyl/volumes", serverId)
    tarBase := restoreDir
    if st, err := os.Stat(volumeSubdir); err == nil && st.IsDir() {
        tarBase = volumeSubdir
    }

    gzFile := filepath.Join(tempDir, serverId+"-"+shortId+".tar.gz")
    _ = os.Remove(gzFile)

    tarCmd := exec.Command("tar", "-czf", gzFile, "-C", tarBase, ".")
    var tarErr bytes.Buffer
    tarCmd.Stderr = &tarErr
    if err := tarCmd.Run(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "tar failed"})
        return err
    }

    _ = os.RemoveAll(restoreDir)
    return nil
}