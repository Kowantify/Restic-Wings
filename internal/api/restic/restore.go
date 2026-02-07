package restic

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

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

    // Async restore to avoid HTTP timeouts behind proxies/CDNs.
    asyncParam := strings.ToLower(strings.TrimSpace(c.Query("async")))
    async := asyncParam == "1" || asyncParam == "true" || asyncParam == "yes"

    // Prevent concurrent restores (best-effort).
    if status, err := readRestoreStatus(serverId); err == nil && status.Status == "running" {
        if status.StartedAt != "" {
            if started, err := time.Parse(time.RFC3339, status.StartedAt); err == nil {
                if time.Since(started) <= 6*time.Hour {
                    c.JSON(http.StatusConflict, gin.H{"error": "restore already running"})
                    return
                }
                status.Status = "failed"
                status.FinishedAt = time.Now().Format(time.RFC3339)
                if status.Message == "" {
                    status.Message = "Restore appears stale. Please retry."
                }
                writeRestoreStatus(serverId, status)
            }
        } else {
            c.JSON(http.StatusConflict, gin.H{"error": "restore already running"})
            return
        }
    }

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    targetPath := fmt.Sprintf("/var/lib/pterodactyl/volumes/%s", serverId)

    run := func() error {
        env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
        // Keep the same command semantics as existing installs to avoid breaking behavior.
        cmdCtx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
        defer cancel()
        cmd := exec.CommandContext(cmdCtx, "restic", "-r", repo, "restore", backupId, "--target", "/", "--path", targetPath)
        cmd.Env = env

        var restoreErr bytes.Buffer
        cmd.Stderr = &restoreErr
        if err := cmd.Run(); err != nil {
            if cmdCtx.Err() == context.DeadlineExceeded {
                return fmt.Errorf("restore timed out")
            }
            detail := strings.TrimSpace(restoreErr.String())
            if detail == "" {
                detail = err.Error()
            }
            return fmt.Errorf("restic restore failed: %s", detail)
        }
        return nil
    }

    if async {
        setRestoreStatus(serverId, "running", "")
        go func() {
            if err := run(); err != nil {
                setRestoreStatus(serverId, "failed", err.Error())
                return
            }
            setRestoreStatus(serverId, "completed", "")
        }()
        c.JSON(http.StatusAccepted, gin.H{"message": "restore started"})
        return
    }

    setRestoreStatus(serverId, "running", "")
    if err := run(); err != nil {
        setRestoreStatus(serverId, "failed", err.Error())
        c.JSON(http.StatusInternalServerError, gin.H{"error": "restic restore failed"})
        return
    }
    setRestoreStatus(serverId, "completed", "")
    c.JSON(http.StatusOK, gin.H{"message": "restore completed"})
}

// GET /api/servers/:server/backups/restic/restore/status
func GetServerResticRestoreStatus(c *gin.Context) {
    serverId := c.Param("server")
    if serverId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing server id"})
        return
    }

    status, err := readRestoreStatus(serverId)
    if err != nil || status.Status == "" {
        c.JSON(http.StatusOK, gin.H{"status": "idle"})
        return
    }

    if status.Status == "running" && status.StartedAt != "" {
        if started, err := time.Parse(time.RFC3339, status.StartedAt); err == nil {
            if time.Since(started) > 6*time.Hour {
                status.Status = "failed"
                status.FinishedAt = time.Now().Format(time.RFC3339)
                if status.Message == "" {
                    status.Message = "Restore appears stale. Please retry."
                }
                writeRestoreStatus(serverId, status)
            }
        }
    }

    c.JSON(http.StatusOK, status)
}

type resticRestoreStatus struct {
    Status     string `json:"status"`
    StartedAt  string `json:"started_at,omitempty"`
    FinishedAt string `json:"finished_at,omitempty"`
    Message    string `json:"message,omitempty"`
}

func restoreStatusDir() string {
    return "/var/lib/pterodactyl/restic/.restore-status"
}

func restoreStatusPath(serverId string) string {
    return filepath.Join(restoreStatusDir(), serverId+".json")
}

func readRestoreStatus(serverId string) (resticRestoreStatus, error) {
    var status resticRestoreStatus
    data, err := os.ReadFile(restoreStatusPath(serverId))
    if err != nil {
        return status, err
    }
    if err := json.Unmarshal(data, &status); err != nil {
        return resticRestoreStatus{}, err
    }
    return status, nil
}

func writeRestoreStatus(serverId string, status resticRestoreStatus) {
    if serverId == "" {
        return
    }
    _ = os.MkdirAll(restoreStatusDir(), 0755)
    data, err := json.Marshal(status)
    if err != nil {
        return
    }
    tmp := restoreStatusPath(serverId) + ".tmp"
    if err := os.WriteFile(tmp, data, 0644); err == nil {
        _ = os.Rename(tmp, restoreStatusPath(serverId))
    }
    // Cleanup: keep files for 7 days (same as backup status).
    cleanupStatusDir(restoreStatusDir(), 7*24*time.Hour)
}

func setRestoreStatus(serverId string, status string, message string) {
    if serverId == "" {
        return
    }
    current, _ := readRestoreStatus(serverId)
    next := resticRestoreStatus{Status: status}

    if status == "running" {
        next.StartedAt = time.Now().Format(time.RFC3339)
        next.FinishedAt = ""
        next.Message = ""
    } else {
        if current.StartedAt != "" {
            next.StartedAt = current.StartedAt
        }
        if status == "completed" || status == "failed" {
            next.FinishedAt = time.Now().Format(time.RFC3339)
        }
        if message != "" {
            // Clamp message size.
            if len(message) > 2000 {
                message = message[:2000]
            }
            next.Message = message
        } else if current.Message != "" {
            next.Message = current.Message
        }
    }
    writeRestoreStatus(serverId, next)
}
