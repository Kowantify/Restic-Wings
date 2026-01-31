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
    var maxBackups int
    if v, ok := c.GetPostForm("owner_username"); ok && v != "" {
        ownerUsername = v
    } else {
        var body struct {
            OwnerUsername string `json:"owner_username"`
            EncryptionKey string `json:"encryption_key"`
            MaxBackups    int    `json:"max_backups"`
        }
        if err := c.ShouldBindJSON(&body); err == nil {
            ownerUsername = body.OwnerUsername
            encryptionKey = body.EncryptionKey
            maxBackups = body.MaxBackups
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

    // Prune oldest backup if maxBackups reached
    if maxBackups > 0 {
        countCmd := exec.Command("restic", "-r", repo, "snapshots", "--json")
        countCmd.Env = env
        countOut, countErr := countCmd.CombinedOutput()
        if countErr == nil {
            var snapshots []map[string]interface{}
            if err := json.Unmarshal(countOut, &snapshots); err == nil {
                if len(snapshots) >= maxBackups {
                    keepLast := maxBackups - 1
                    if keepLast < 1 {
                        keepLast = 1
                    }
                    pruneCmd := exec.Command("restic", "-r", repo, "forget", "--prune", "--keep-last", strconv.Itoa(keepLast))
                    pruneCmd.Env = env
                    if out, err := pruneCmd.CombinedOutput(); err != nil {
                        c.JSON(http.StatusInternalServerError, gin.H{"error": "prune failed", "output": string(out)})
                        return
                    }
                }
            }
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
        // If repo missing/uninitialized, initialize and return empty list
        if _, statErr := os.Stat(repo + "/config"); os.IsNotExist(statErr) {
            initCmd := exec.Command("restic", "-r", repo, "init")
            initCmd.Env = env
            if initOut, initErr := initCmd.CombinedOutput(); initErr == nil {
                c.JSON(http.StatusOK, gin.H{
                    "backups":     []map[string]interface{}{},
                    "next_cursor": "",
                    "limit":       0,
                    "total":       0,
                })
                return
            } else {
                c.JSON(http.StatusInternalServerError, gin.H{"error": "init failed", "output": string(initOut)})
                return
            }
        }
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
        } else if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
            sinceTime = t
            sinceOk = true
        } else if t, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local); err == nil {
            sinceTime = t
            sinceOk = true
        }
    }
    if untilStr != "" {
        if t, err := time.Parse(time.RFC3339Nano, untilStr); err == nil {
            untilTime = t
            untilOk = true
        } else if t, err := time.Parse(time.RFC3339, untilStr); err == nil {
            untilTime = t
            untilOk = true
        } else if t, err := time.ParseInLocation("2006-01-02", untilStr, time.Local); err == nil {
            untilTime = t.Add(24*time.Hour - time.Nanosecond)
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

    filteredAll := make([]snapshotItem, 0, len(items))
    filtered := make([]snapshotItem, 0, len(items))
    for _, item := range items {
        if sinceOk && !item.Time.IsZero() && item.Time.Before(sinceTime) {
            continue
        }
        if untilOk && !item.Time.IsZero() && item.Time.After(untilTime) {
            continue
        }
        filteredAll = append(filteredAll, item)
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
        "total":       len(filteredAll),
    })
}

// GET /api/servers/:server/backups/restic/stats
func GetServerResticStats(c *gin.Context) {
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

    if _, err := os.Stat(repo + "/config"); os.IsNotExist(err) {
        if err := os.MkdirAll(repo, 0755); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create repo dir", "details": err.Error()})
            return
        }
        initCmd := exec.Command("restic", "-r", repo, "init")
        initCmd.Env = env
        if out, err := initCmd.CombinedOutput(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "init failed", "output": string(out)})
            return
        }
        c.JSON(http.StatusOK, gin.H{"total_size": 0})
        return
    }

    statsCmd := exec.Command("restic", "-r", repo, "stats", "--json")
    statsCmd.Env = env
    out, err := statsCmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats", "output": string(out)})
        return
    }

    var stats map[string]interface{}
    if err := json.Unmarshal(out, &stats); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse restic output", "output": string(out)})
        return
    }

    extractNumber := func(val interface{}) (float64, bool) {
        switch t := val.(type) {
        case float64:
            return t, true
        case float32:
            return float64(t), true
        case int:
            return float64(t), true
        case int64:
            return float64(t), true
        case json.Number:
            if f, err := t.Float64(); err == nil {
                return f, true
            }
        case map[string]interface{}:
            if v, ok := t["bytes"]; ok {
                return extractNumber(v)
            }
        }
        return 0, false
    }

    keys := []string{"total_size", "total_file_size", "total_uncompressed_size", "total_blob_size", "repository_size"}
    for _, key := range keys {
        if v, ok := stats[key]; ok {
            if n, ok := extractNumber(v); ok {
                stats["repo_size"] = n
                break
            }
        }
    }

    c.JSON(http.StatusOK, stats)
}
