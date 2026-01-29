package restic

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "os/exec"

    "github.com/gin-gonic/gin"
)

// POST /api/servers/:server/backups/restic
func CreateServerResticBackup(c *gin.Context) {
    serverId := c.Param("server")
    if serverId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing server id"})
        return
    }

    var ownerUsername, encryptionKey string
    if v, ok := c.GetPostForm("owner_username"); ok && v != "" {
        ownerUsername = v
    } else {
        var body struct {
            OwnerUsername string `json:"owner_username"`
            EncryptionKey string `json:"encryption_key"`
        }
        if err := c.ShouldBindJSON(&body); err == nil {
            ownerUsername = body.OwnerUsername
            encryptionKey = body.EncryptionKey
        }
    }
    if encryptionKey == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing encryption key"})
        return
    }

    repoDir := serverId
    if ownerUsername != "" {
        repoDir = fmt.Sprintf("%s+%s", serverId, ownerUsername)
    }

    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    if err := os.MkdirAll(repo, 0755); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create repo dir", "details": err.Error()})
        return
    }

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)

    // Init repo if needed
    if _, err := os.Stat(repo + "/config"); os.IsNotExist(err) {
        initCmd := exec.Command("restic", "-r", repo, "init")
        initCmd.Env = env
        if out, err := initCmd.CombinedOutput(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "init failed", "output": string(out)})
            return
        }
    }

    volumePath := fmt.Sprintf("/var/lib/pterodactyl/volumes/%s", serverId)
    cmd := exec.Command("restic", "-r", repo, "backup", volumePath)
    cmd.Env = env
    out, err := cmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "backup failed", "output": string(out)})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "backup created", "output": string(out)})
}

// GET /api/servers/:server/backups/restic
func ListServerResticBackups(c *gin.Context) {
    serverId := c.Param("server")
    if serverId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing server id"})
        return
    }

    var ownerUsername, encryptionKey string
    if v, ok := c.GetQuery("owner_username"); ok && v != "" {
        ownerUsername = v
    } else {
        ownerUsername = c.Query("owner_username")
    }
    if v, ok := c.GetQuery("encryption_key"); ok && v != "" {
        encryptionKey = v
    } else {
        encryptionKey = c.Query("encryption_key")
    }
    if encryptionKey == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing encryption key"})
        return
    }

    repoDir := serverId
    if ownerUsername != "" {
        repoDir = fmt.Sprintf("%s+%s", serverId, ownerUsername)
    }
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)

    // List snapshots
    cmd := exec.Command("restic", "-r", repo, "snapshots", "--json")
    cmd.Env = env
    out, err := cmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list backups", "output": string(out)})
        return
    }

    // Parse JSON output
    var snapshots []map[string]interface{}
    if err := json.Unmarshal(out, &snapshots); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse restic output", "output": string(out)})
        return
    }
    c.JSON(http.StatusOK, gin.H{"backups": snapshots})
}
