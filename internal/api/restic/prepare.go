package restic

import (
    "bytes"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/pterodactyl/wings/server"
)

// POST /api/servers/:server/backups/restic/:backupId/prepare
func PrepareServerResticBackupHandler(c *gin.Context) {
    backupId := c.Param("backupId")
    log.Printf("restic prepare handler hit server=%s backup=%s", c.Param("server"), backupId)
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
    asyncParam := strings.ToLower(strings.TrimSpace(c.Query("async")))
    async := asyncParam == "1" || asyncParam == "true" || asyncParam == "yes"

    if backupId == "" {
        backupId = c.Query("backup_id")
    }
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup_id"})
        return
    }
    if encryptionKey == "" || ownerUsername == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing encryption_key or owner_username"})
        return
    }

    if async {
        setDownloadStatus(s.ID(), backupId, "running", "")
        serverId := s.ID()
        go func() {
            if err := prepareServerResticBackupInternal(serverId, backupId, encryptionKey, ownerUsername); err != nil {
                setDownloadStatus(serverId, backupId, "failed", err.Error())
                return
            }
            setDownloadStatus(serverId, backupId, "ready", "")
        }()
        c.JSON(http.StatusAccepted, gin.H{"message": "preparing"})
        return
    }

    if err := prepareServerResticBackupInternal(s.ID(), backupId, encryptionKey, ownerUsername); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "prepared"})
}

// GET /api/servers/:server/backups/restic/:backupId/prepare/status
func GetServerResticBackupPrepareStatus(c *gin.Context) {
    backupId := c.Param("backupId")
    if backupId == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing backup id"})
        return
    }
    s := c.MustGet("server").(*server.Server)
    status, err := readDownloadStatus(s.ID(), backupId)
    if err != nil || status.Status == "" {
        c.JSON(http.StatusOK, gin.H{"status": "idle"})
        return
    }
    c.JSON(http.StatusOK, status)
}

type resticDownloadStatus struct {
    Status     string `json:"status"`
    StartedAt  string `json:"started_at,omitempty"`
    FinishedAt string `json:"finished_at,omitempty"`
    Message    string `json:"message,omitempty"`
}

func downloadStatusDir() string {
    return "/var/lib/pterodactyl/restic/.download-status"
}

func downloadStatusPath(serverId string, backupId string) string {
    return filepath.Join(downloadStatusDir(), serverId+"-"+backupId+".json")
}

func readDownloadStatus(serverId string, backupId string) (resticDownloadStatus, error) {
    var status resticDownloadStatus
    data, err := os.ReadFile(downloadStatusPath(serverId, backupId))
    if err != nil {
        return status, err
    }
    if err := json.Unmarshal(data, &status); err != nil {
        return resticDownloadStatus{}, err
    }
    return status, nil
}

func writeDownloadStatus(serverId string, backupId string, status resticDownloadStatus) {
    if serverId == "" || backupId == "" {
        return
    }
    _ = os.MkdirAll(downloadStatusDir(), 0755)
    data, err := json.Marshal(status)
    if err != nil {
        return
    }
    tmp := downloadStatusPath(serverId, backupId) + ".tmp"
    if err := os.WriteFile(tmp, data, 0644); err == nil {
        _ = os.Rename(tmp, downloadStatusPath(serverId, backupId))
    }
    cleanupDownloadStatusDir(downloadStatusDir(), 7*24*time.Hour)
}

func prepareLog(message string) {
    line := "[" + time.Now().Format(time.RFC3339) + "] " + message + "\n"
    log.Printf("restic prepare: %s", message)
    _ = os.MkdirAll("/var/lib/pterodactyl/restic", 0755)
    if f, err := os.OpenFile("/var/lib/pterodactyl/restic/prepare.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
        _, _ = f.WriteString(line)
        _ = f.Close()
    }
}

func cleanupDownloadStatusDir(dir string, maxAge time.Duration) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return
    }
    now := time.Now()
    for _, entry := range entries {
        info, err := entry.Info()
        if err != nil {
            continue
        }
        if now.Sub(info.ModTime()) > maxAge {
            _ = os.Remove(filepath.Join(dir, entry.Name()))
        }
    }
}

func setDownloadStatus(serverId string, backupId string, status string, message string) {
    if serverId == "" || backupId == "" {
        return
    }
    current, _ := readDownloadStatus(serverId, backupId)
    next := resticDownloadStatus{
        Status: status,
    }
    if current.StartedAt != "" {
        next.StartedAt = current.StartedAt
    }
    if status == "running" && next.StartedAt == "" {
        next.StartedAt = time.Now().Format(time.RFC3339)
    }
    if status == "ready" || status == "failed" {
        next.FinishedAt = time.Now().Format(time.RFC3339)
    }
    if message != "" {
        next.Message = message
    }
    writeDownloadStatus(serverId, backupId, next)
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
    if err := prepareServerResticBackupInternal(serverId, backupId, encryptionKey, ownerUsername); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "prepare failed"})
        return err
    }
    return nil
}

func preparedArchivePath(serverId, backupId string) string {
    tempDir := "/var/lib/pterodactyl/restic/temp"
    sum := sha256.Sum256([]byte(backupId))
    short := hex.EncodeToString(sum[:8])
    return filepath.Join(tempDir, serverId+"-"+short+".tar.zst")
}

func prepareServerResticBackupInternal(serverId, backupId, encryptionKey, ownerUsername string) error {
    if backupId == "" {
        return fmt.Errorf("missing backup_id")
    }
    if encryptionKey == "" || ownerUsername == "" {
        return fmt.Errorf("missing encryption_key or owner_username")
    }

    prepareLog("prepare start server=" + serverId + " backup=" + backupId)

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    tempDir := "/var/lib/pterodactyl/restic/temp"
    if err := os.MkdirAll(tempDir, 0700); err != nil {
        return err
    }

    sum := sha256.Sum256([]byte(backupId))
    short := hex.EncodeToString(sum[:8])

    restoreDir := filepath.Join(tempDir, serverId+"-"+short+"-restore")
    _ = os.RemoveAll(restoreDir)

    env := append(os.Environ(), "RESTIC_PASSWORD="+encryptionKey)
    restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 2*time.Hour)
    defer restoreCancel()
    restoreCmd := exec.CommandContext(restoreCtx, "restic", "-r", repo, "restore", backupId, "--target", restoreDir)
    restoreCmd.Env = env

    var restoreErr bytes.Buffer
    restoreCmd.Stderr = &restoreErr
    if err := restoreCmd.Run(); err != nil {
        _ = os.RemoveAll(restoreDir)
        if restoreCtx.Err() == context.DeadlineExceeded {
            prepareLog("restore timeout server=" + serverId + " backup=" + backupId)
            return fmt.Errorf("restore timed out")
        }
        detail := strings.TrimSpace(restoreErr.String())
        if detail == "" {
            detail = err.Error()
        }
        prepareLog("restore failed server=" + serverId + " backup=" + backupId + " error=" + detail)
        return fmt.Errorf("restic restore failed: %s", detail)
    }

    volumeSubdir := filepath.Join(restoreDir, "var/lib/pterodactyl/volumes", serverId)
    tarBase := restoreDir
    if st, err := os.Stat(volumeSubdir); err == nil && st.IsDir() {
        tarBase = volumeSubdir
    }

    tarZstFile := preparedArchivePath(serverId, backupId)
    if st, err := os.Stat(tarZstFile); err == nil && st.Size() > 0 {
        _ = os.RemoveAll(restoreDir)
        prepareLog("prepare reuse server=" + serverId + " backup=" + backupId + " file=" + tarZstFile)
        return nil
    }
    _ = os.Remove(tarZstFile)

    tarCtx, tarCancel := context.WithTimeout(context.Background(), 2*time.Hour)
    defer tarCancel()
    tarCmd := exec.CommandContext(tarCtx, "tar", "-I", "zstd -3 -T0", "-cf", tarZstFile, "-C", tarBase, ".")
    var tarErr bytes.Buffer
    tarCmd.Stderr = &tarErr
    if err := tarCmd.Run(); err != nil {
        _ = os.RemoveAll(restoreDir)
        _ = os.Remove(tarZstFile)
        if tarCtx.Err() == context.DeadlineExceeded {
            prepareLog("archive timeout server=" + serverId + " backup=" + backupId)
            return fmt.Errorf("archive timed out")
        }
        detail := strings.TrimSpace(tarErr.String())
        if detail == "" {
            detail = err.Error()
        }
        prepareLog("archive failed server=" + serverId + " backup=" + backupId + " error=" + detail)
        return fmt.Errorf("archive failed: %s", detail)
    }

    _ = os.RemoveAll(restoreDir)
    prepareLog("prepare ok server=" + serverId + " backup=" + backupId + " file=" + tarZstFile)
    return nil
}