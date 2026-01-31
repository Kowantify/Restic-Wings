package restic

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "sort"
    "strconv"
    "time"

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

    // Pagination + filtering
    limit := 25
    if rawLimit := c.Query("limit"); rawLimit != "" {
        if v, err := strconv.Atoi(rawLimit); err == nil && v > 0 {
            if v > 100 {
                v = 100
            }
            limit = v
        }
    }

    parseSnapshotTime := func(val interface{}) time.Time {
        s, _ := val.(string)
        if s == "" {
            return time.Time{}
        }
        if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
            return t
        }
        if t, err := time.Parse(time.RFC3339, s); err == nil {
            return t
        }
        return time.Time{}
    }

    sinceStr := c.Query("since")
    untilStr := c.Query("until")
    var sinceTime time.Time
    var untilTime time.Time
    var sinceOk bool
    var untilOk bool
    if sinceStr != "" {
        if t, err := time.Parse(time.RFC3339Nano, sinceStr); err == nil {
            sinceTime = t
            sinceOk = true
        } else if t, err := time.Parse("2006-01-02", sinceStr); err == nil {
            sinceTime = t
            sinceOk = true
        }
    }
    if untilStr != "" {
        if t, err := time.Parse(time.RFC3339Nano, untilStr); err == nil {
            untilTime = t
            untilOk = true
        } else if t, err := time.Parse("2006-01-02", untilStr); err == nil {
            untilTime = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
            untilOk = true
        }
    }

    cursorStr := c.Query("cursor")
    var cursorTime time.Time
    var cursorOk bool
    if cursorStr != "" {
        if t, err := time.Parse(time.RFC3339Nano, cursorStr); err == nil {
            cursorTime = t
            cursorOk = true
        } else if t, err := time.Parse(time.RFC3339, cursorStr); err == nil {
            cursorTime = t
            cursorOk = true
        }
    }

    type snapshotItem struct {
        Raw  map[string]interface{}
        Time time.Time
    }

    items := make([]snapshotItem, 0, len(snapshots))
    for _, snap := range snapshots {
        items = append(items, snapshotItem{Raw: snap, Time: parseSnapshotTime(snap["time"])})
    }

    sort.Slice(items, func(i, j int) bool {
        return items[i].Time.After(items[j].Time)
    })

    filtered := make([]snapshotItem, 0, len(items))
    for _, item := range items {
        if sinceOk && !item.Time.IsZero() && item.Time.Before(sinceTime) {
            continue
        }
        if untilOk && !item.Time.IsZero() && item.Time.After(untilTime) {
            continue
        }
        if cursorOk && !item.Time.IsZero() && (item.Time.Equal(cursorTime) || item.Time.After(cursorTime)) {
            continue
        }
        filtered = append(filtered, item)
    }

    page := make([]map[string]interface{}, 0, limit)
    for i, item := range filtered {
        if i >= limit {
            break
        }
        page = append(page, item.Raw)
    }

    var nextCursor string
    if len(filtered) > limit {
        last := page[len(page)-1]
        if last != nil {
            if t, ok := last["time"].(string); ok {
                nextCursor = t
            }
        }
    }

    c.JSON(http.StatusOK, gin.H{
        "backups":     page,
        "next_cursor": nextCursor,
        "limit":       limit,
    })
}
