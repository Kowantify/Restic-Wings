package restic

import (
    "bytes"
    "fmt"
    "net/http"
    "os"
    "os/exec"

    "github.com/gin-gonic/gin"
    "github.com/pterodactyl/wings/server"
)

// POST /api/servers/:server/backups/restic/:backupId/restore
func RestoreServerResticBackupHandler(c *gin.Context) {
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
    if backupId == "" || encryptionKey == "" || ownerUsername == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup_id, encryption_key, or owner_username"})
        return
    }

    s := c.MustGet("server").(*server.Server)
    serverId := s.ID()
    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    targetPath := fmt.Sprintf("/var/lib/pterodactyl/volumes/%s", serverId)

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    cmd := exec.Command("restic", "-r", repo, "restore", backupId, "--target", "/", "--path", targetPath)
    cmd.Env = env

    var restoreErr bytes.Buffer
    cmd.Stderr = &restoreErr
    if err := cmd.Run(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic restore failed"})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "restore completed"})
}