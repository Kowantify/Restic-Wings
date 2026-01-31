package restic

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "strings"

    "github.com/gin-gonic/gin"
)

// Helper to resolve repo path/env from request
func resticRepoFromRequest(c *gin.Context) (string, []string, error) {
    serverId := c.Param("server")
    if serverId == "" {
        return "", nil, fmt.Errorf("missing server id")
    }

    var ownerUsername, encryptionKey string
    if v, ok := c.GetPostForm("owner_username"); ok && v != "" {
        ownerUsername = v
    }
    if v, ok := c.GetPostForm("encryption_key"); ok && v != "" {
        encryptionKey = v
    }

    if ownerUsername == "" || encryptionKey == "" {
        var body struct {
            OwnerUsername string `json:"owner_username"`
            EncryptionKey string `json:"encryption_key"`
        }
        if err := c.ShouldBindJSON(&body); err == nil {
            if ownerUsername == "" {
                ownerUsername = body.OwnerUsername
            }
            if encryptionKey == "" {
                encryptionKey = body.EncryptionKey
            }
        }
    }

    if encryptionKey == "" {
        return "", nil, fmt.Errorf("missing encryption key")
    }

    repoDir := serverId
    if ownerUsername != "" {
        repoDir = fmt.Sprintf("%s+%s", serverId, ownerUsername)
    }
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    return repo, env, nil
}

// POST /api/servers/:server/backups/restic/:backupId/lock
func LockServerResticBackup(c *gin.Context) {
    backupId := c.Param("backupId")
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup id"})
        return
    }

    repo, env, err := resticRepoFromRequest(c)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    resolvedId := resolveSnapshotID(repo, env, backupId)
    tagCmd := exec.Command("restic", "-r", repo, "tag", "--add", "locked", resolvedId)
    tagCmd.Env = env
    out, err := tagCmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to lock backup", "output": string(out)})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "locked", "locked": true})
}

// POST /api/servers/:server/backups/restic/:backupId/unlock
func UnlockServerResticBackup(c *gin.Context) {
    backupId := c.Param("backupId")
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup id"})
        return
    }

    repo, env, err := resticRepoFromRequest(c)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    resolvedId := resolveSnapshotID(repo, env, backupId)
    tagCmd := exec.Command("restic", "-r", repo, "tag", "--remove", "locked", resolvedId)
    tagCmd.Env = env
    out, err := tagCmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unlock backup", "output": string(out)})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "unlocked", "locked": false})
}

// DELETE /api/servers/:server/backups/restic/repo
func DeleteServerResticRepo(c *gin.Context) {
    serverId := c.Param("server")
    if serverId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing server id"})
        return
    }

    base := "/var/lib/pterodactyl/restic/"
    entries, err := os.ReadDir(base)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read repo dir", "details": err.Error()})
        return
    }

    deleted := 0
    for _, entry := range entries {
        name := entry.Name()
        if name == serverId || strings.HasPrefix(name, serverId+"+") {
            path := base + name
            if err := os.RemoveAll(path); err != nil {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete repo", "details": err.Error()})
                return
            }
            deleted++
        }
    }

    c.JSON(http.StatusOK, gin.H{"message": "repo deleted", "deleted": deleted})
}

func resolveSnapshotID(repo string, env []string, backupId string) string {
    if backupId == "" {
        return ""
    }
    listCmd := exec.Command("restic", "-r", repo, "snapshots", "--json")
    listCmd.Env = env
    out, err := listCmd.CombinedOutput()
    if err != nil {
        return backupId
    }
    var snapshots []map[string]interface{}
    if err := json.Unmarshal(out, &snapshots); err != nil {
        return backupId
    }
    for _, snap := range snapshots {
        if id, ok := snap["id"].(string); ok && id != "" {
            if id == backupId || (len(id) >= 8 && id[:8] == backupId) {
                return id
            }
        }
        if shortID, ok := snap["short_id"].(string); ok && shortID != "" && shortID == backupId {
            if id, ok := snap["id"].(string); ok && id != "" {
                return id
            }
        }
    }
    return backupId
}