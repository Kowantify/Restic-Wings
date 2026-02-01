package restic

import (
    "bytes"
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
    asyncParam := strings.ToLower(strings.TrimSpace(c.Query("async")))
    async := asyncParam == "1" || asyncParam == "true" || asyncParam == "yes"

    if async {
        setDownloadStatus(s.ID(), backupId, "running", "")
        go func() {
            if err := PrepareServerResticBackup(c, s, backupId, encryptionKey, ownerUsername); err != nil {
                setDownloadStatus(s.ID(), backupId, "failed", "prepare failed")
                return
            }
            setDownloadStatus(s.ID(), backupId, "ready", "")
        }()
        c.JSON(http.StatusAccepted, gin.H{"message": "preparing"})
        return
    }

    if err := PrepareServerResticBackup(c, s, backupId, encryptionKey, ownerUsername); err != nil {
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

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
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

    tarZstFile := filepath.Join(tempDir, serverId+"-"+shortId+".tar.zst")
    _ = os.Remove(tarZstFile)

    tarCmd := exec.Command("tar", "-I", "zstd -3 -T0", "-cf", tarZstFile, "-C", tarBase, ".")
    var tarErr bytes.Buffer
    tarCmd.Stderr = &tarErr
    if err := tarCmd.Run(); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "tar failed"})
        return err
    }

    _ = os.RemoveAll(restoreDir)
    return nil
}