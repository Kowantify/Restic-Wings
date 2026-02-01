package restic

import (
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "sort"
    "strconv"
    "strings"
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
    var maxRepoBytes int64
    if v, ok := c.GetPostForm("owner_username"); ok && v != "" {
        ownerUsername = v
    } else {
        var body struct {
            OwnerUsername string `json:"owner_username"`
            EncryptionKey string `json:"encryption_key"`
            MaxBackups    int    `json:"max_backups"`
            MaxRepoBytes  int64  `json:"max_repo_bytes"`
        }
        if err := c.ShouldBindJSON(&body); err == nil {
            ownerUsername = body.OwnerUsername
            encryptionKey = body.EncryptionKey
            maxBackups = body.MaxBackups
            maxRepoBytes = body.MaxRepoBytes
        }
    }
    if encryptionKey == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "missing encryption key"})
        return
    }

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    if err := os.MkdirAll(repo, 0755); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create repo dir", "details": err.Error()})
        return
    }
    ensureResticKeyFile(repo, encryptionKey)

    env := buildResticEnv(encryptionKey)

    // Init repo if needed
    if _, err := os.Stat(repo + "/config"); os.IsNotExist(err) {
        initCmd := exec.Command("restic", "-r", repo, "init")
        initCmd.Env = env
        if out, err := initCmd.CombinedOutput(); err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "init failed", "output": string(out)})
            return
        }
    }

    if maxRepoBytes > 0 {
        statsCmd := exec.Command("restic", "-r", repo, "stats", "--json")
        statsCmd.Env = env
        statsOut, statsErr := statsCmd.CombinedOutput()
        if statsErr == nil {
            var stats map[string]interface{}
            if err := json.Unmarshal(statsOut, &stats); err == nil {
                if v, ok := stats["total_size"]; ok {
                    switch t := v.(type) {
                    case float64:
                        if int64(t) >= maxRepoBytes {
                            c.JSON(http.StatusBadRequest, gin.H{"error": "repo size limit reached"})
                            return
                        }
                    case json.Number:
                        if n, err := t.Int64(); err == nil && n >= maxRepoBytes {
                            c.JSON(http.StatusBadRequest, gin.H{"error": "repo size limit reached"})
                            return
                        }
                    }
                }
            }
        }
    }

    // Prune oldest backup if maxBackups reached (keep locked snapshots)
    if maxBackups > 0 {
        countCmd := exec.Command("restic", "-r", repo, "snapshots", "--json")
        countCmd.Env = env
        countOut, countErr := countCmd.CombinedOutput()
        if countErr == nil {
            var snapshots []map[string]interface{}
            if err := json.Unmarshal(countOut, &snapshots); err == nil {
                if len(snapshots) >= maxBackups {
                    // Build locked set (prefer tags in snapshot list; fallback to --tag when tags missing)
                    lockedIDs := map[string]bool{}
                    sawTags := false
                    hasLockedTag := func(tags interface{}) bool {
                        switch v := tags.(type) {
                        case []interface{}:
                            for _, t := range v {
                                if s, ok := t.(string); ok && s == "locked" {
                                    return true
                                }
                            }
                        case []string:
                            for _, s := range v {
                                if s == "locked" {
                                    return true
                                }
                            }
                        }
                        return false
                    }
                    for _, snap := range snapshots {
                        if tags, ok := snap["tags"]; ok {
                            sawTags = true
                            if hasLockedTag(tags) {
                                if id, ok := snap["id"].(string); ok && id != "" {
                                    lockedIDs[id] = true
                                    if len(id) >= 8 {
                                        lockedIDs[id[:8]] = true
                                    }
                                }
                                if shortID, ok := snap["short_id"].(string); ok && shortID != "" {
                                    lockedIDs[shortID] = true
                                }
                            }
                        }
                    }
                    if !sawTags {
                        lockCmd := exec.Command("restic", "-r", repo, "snapshots", "--json", "--tag", "locked")
                        lockCmd.Env = env
                        if lockOut, lockErr := lockCmd.CombinedOutput(); lockErr == nil {
                            var lockedSnapshots []map[string]interface{}
                            if err := json.Unmarshal(lockOut, &lockedSnapshots); err == nil {
                                for _, snap := range lockedSnapshots {
                                    if id, ok := snap["id"].(string); ok && id != "" {
                                        lockedIDs[id] = true
                                        if len(id) >= 8 {
                                            lockedIDs[id[:8]] = true
                                        }
                                    }
                                    if shortID, ok := snap["short_id"].(string); ok && shortID != "" {
                                        lockedIDs[shortID] = true
                                    }
                                }
                            }
                        }
                    }

                    type snapItem struct {
                        ID   string
                        Time time.Time
                    }

                    parseTime := func(val interface{}) time.Time {
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

                    unlocked := make([]snapItem, 0, len(snapshots))
                    for _, snap := range snapshots {
                        id, _ := snap["id"].(string)
                        shortID, _ := snap["short_id"].(string)
                        if id != "" && len(id) >= 8 && shortID == "" {
                            shortID = id[:8]
                        }
                        if (id != "" && lockedIDs[id]) || (shortID != "" && lockedIDs[shortID]) {
                            continue
                        }
                        if id == "" {
                            continue
                        }
                        unlocked = append(unlocked, snapItem{ID: id, Time: parseTime(snap["time"])})
                    }

                    sort.Slice(unlocked, func(i, j int) bool {
                        return unlocked[i].Time.Before(unlocked[j].Time)
                    })

                    if len(unlocked) == 0 {
                        c.JSON(http.StatusBadRequest, gin.H{"error": "backup limit reached and all snapshots are locked"})
                        return
                    }

                    toDelete := len(snapshots) - maxBackups + 1
                    if toDelete < 1 {
                        toDelete = 1
                    }

                    for i := 0; i < toDelete && i < len(unlocked); i++ {
                        pruneCmd := exec.Command("restic", "-r", repo, "forget", unlocked[i].ID, "--prune")
                        pruneCmd.Env = env
                        if out, err := pruneCmd.CombinedOutput(); err != nil {
                            c.JSON(http.StatusInternalServerError, gin.H{"error": "prune failed", "output": string(out)})
                            return
                        }
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

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)
    ensureResticKeyFile(repo, encryptionKey)

    env := buildResticEnv(encryptionKey)

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

    // Detect locked snapshots by tags in list (fallback to --tag when tags missing)
    lockedIDs := map[string]bool{}
    sawTags := false
    hasLockedTag := func(tags interface{}) bool {
        switch v := tags.(type) {
        case []interface{}:
            for _, t := range v {
                if s, ok := t.(string); ok && s == "locked" {
                    return true
                }
            }
        case []string:
            for _, s := range v {
                if s == "locked" {
                    return true
                }
            }
        }
        return false
    }
    for _, snap := range snapshots {
        if tags, ok := snap["tags"]; ok {
            sawTags = true
            if hasLockedTag(tags) {
                if id, ok := snap["id"].(string); ok && id != "" {
                    lockedIDs[id] = true
                    if len(id) >= 8 {
                        lockedIDs[id[:8]] = true
                    }
                }
                if shortID, ok := snap["short_id"].(string); ok && shortID != "" {
                    lockedIDs[shortID] = true
                }
            }
        }
    }
    if !sawTags {
        lockCmd := exec.Command("restic", "-r", repo, "snapshots", "--json", "--tag", "locked")
        lockCmd.Env = env
        if lockOut, lockErr := lockCmd.CombinedOutput(); lockErr == nil {
            var lockedSnapshots []map[string]interface{}
            if err := json.Unmarshal(lockOut, &lockedSnapshots); err == nil {
                for _, snap := range lockedSnapshots {
                    if id, ok := snap["id"].(string); ok && id != "" {
                        lockedIDs[id] = true
                        if len(id) >= 8 {
                            lockedIDs[id[:8]] = true
                        }
                    }
                    if shortID, ok := snap["short_id"].(string); ok && shortID != "" {
                        lockedIDs[shortID] = true
                    }
                }
            }
        }
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
        id, _ := snap["id"].(string)
        shortID, _ := snap["short_id"].(string)
        if id != "" && len(id) >= 8 && shortID == "" {
            shortID = id[:8]
        }

        isLocked := false
        if sawTags {
            if tags, ok := snap["tags"]; ok {
                isLocked = hasLockedTag(tags)
            }
        } else {
            isLocked = (id != "" && lockedIDs[id]) || (shortID != "" && lockedIDs[shortID])
        }
        if isLocked {
            if tags, ok := snap["tags"].([]interface{}); ok {
                hasLocked := false
                for _, t := range tags {
                    if s, ok := t.(string); ok && s == "locked" {
                        hasLocked = true
                        break
                    }
                }
                if !hasLocked {
                    snap["tags"] = append(tags, "locked")
                }
            } else {
                snap["tags"] = []string{"locked"}
            }
        }
        if isLocked {
            snap["locked"] = true
        } else {
            snap["locked"] = false
        }
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

func ensureResticKeyFile(repo string, encryptionKey string) {
    if repo == "" || encryptionKey == "" {
        return
    }
    keyPath := filepath.Join(repo, ".restic-key")
    _ = os.WriteFile(keyPath, []byte(encryptionKey+"\n"), 0600)
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

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)

    env := buildResticEnv(encryptionKey)

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

    runStats := func(mode string) (map[string]interface{}, error) {
        args := []string{"-r", repo, "stats", "--json"}
        if mode != "" {
            args = append(args, "--mode", mode)
        }
        cmd := exec.Command("restic", args...)
        cmd.Env = env
        out, err := cmd.CombinedOutput()
        if err != nil {
            return nil, fmt.Errorf("%s", string(out))
        }
        var parsed map[string]interface{}
        if err := json.Unmarshal(out, &parsed); err != nil {
            return nil, fmt.Errorf("%s", string(out))
        }
        return parsed, nil
    }

    stats, err := runStats("")
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats", "output": err.Error()})
        return
    }

    rawStats, rawErr := runStats("raw-data")
    restoreStats, restoreErr := runStats("restore-size")

    var extractNumber func(interface{}) (float64, bool)
    extractNumber = func(val interface{}) (float64, bool) {
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

    response := gin.H{}

    // Compressed/encrypted on disk: prefer raw-data total_size
    if rawErr == nil {
        if v, ok := rawStats["total_size"]; ok {
            if n, ok := extractNumber(v); ok {
                response["total_size"] = n
            }
        }
    }
    if _, ok := response["total_size"]; !ok {
        if v, ok := stats["total_size"]; ok {
            if n, ok := extractNumber(v); ok {
                response["total_size"] = n
            }
        }
    }

    // Uncompressed/restore size: prefer restore-size total_size
    if restoreErr == nil {
        if v, ok := restoreStats["total_size"]; ok {
            if n, ok := extractNumber(v); ok {
                response["total_uncompressed_size"] = n
            }
        }
    }
    if _, ok := response["total_uncompressed_size"]; !ok {
        if v, ok := stats["total_uncompressed_size"]; ok {
            if n, ok := extractNumber(v); ok {
                response["total_uncompressed_size"] = n
            }
        } else if v, ok := stats["total_file_size"]; ok {
            if n, ok := extractNumber(v); ok {
                response["total_uncompressed_size"] = n
            }
        }
    }

    if v, ok := stats["snapshots_count"]; ok {
        if n, ok := extractNumber(v); ok {
            response["snapshots_count"] = n
        }
    }

    c.JSON(http.StatusOK, response)
}

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

    repoDir := resolveRepoDir(serverId, ownerUsername)
    repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", repoDir)

    if encryptionKey == "" {
        return "", nil, fmt.Errorf("missing encryption key")
    }

    env := buildResticEnv(encryptionKey)
    return repo, env, nil
}

func buildResticEnv(encryptionKey string) []string {
    base := os.Environ()
    filtered := make([]string, 0, len(base)+1)
    for _, v := range base {
        if strings.HasPrefix(v, "RESTIC_PASSWORD=") || strings.HasPrefix(v, "RESTIC_PASSWORD_FILE=") {
            continue
        }
        filtered = append(filtered, v)
    }
    filtered = append(filtered, "RESTIC_PASSWORD="+encryptionKey)
    return filtered
}

func resolveRepoDir(serverId string, ownerUsername string) string {
    candidates := []string{}
    if ownerUsername != "" {
        candidates = append(candidates, fmt.Sprintf("%s+%s", serverId, ownerUsername))
    }
    candidates = append(candidates, serverId)

    for _, dir := range candidates {
        repo := fmt.Sprintf("/var/lib/pterodactyl/restic/%s", dir)
        if repoExists(repo) {
            return dir
        }
    }
    if ownerUsername != "" {
        return fmt.Sprintf("%s+%s", serverId, ownerUsername)
    }
    return serverId
}

func repoExists(repo string) bool {
    if repo == "" {
        return false
    }
    if _, err := os.Stat(filepath.Join(repo, "config")); err == nil {
        return true
    }
    if info, err := os.Stat(repo); err == nil && info.IsDir() {
        return true
    }
    return false
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

// POST /api/servers/:server/backups/restic/prune
func PruneServerResticBackup(c *gin.Context) {
    repo, env, err := resticRepoFromRequest(c)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    var body struct {
        KeepLast   int    `json:"keep_last"`
        KeepDaily  int    `json:"keep_daily"`
        KeepWeekly int    `json:"keep_weekly"`
        KeepMonthly int   `json:"keep_monthly"`
        KeepYearly int    `json:"keep_yearly"`
        KeepWithin string `json:"keep_within"`
    }
    _ = c.ShouldBindJSON(&body)

    if body.KeepLast <= 0 && body.KeepDaily <= 0 && body.KeepWeekly <= 0 && body.KeepMonthly <= 0 && body.KeepYearly <= 0 && strings.TrimSpace(body.KeepWithin) == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "at least one retention rule is required"})
        return
    }

    args := []string{"-r", repo, "forget", "--prune", "--keep-tag", "locked"}
    if body.KeepLast > 0 {
        args = append(args, "--keep-last", strconv.Itoa(body.KeepLast))
    }
    if body.KeepDaily > 0 {
        args = append(args, "--keep-daily", strconv.Itoa(body.KeepDaily))
    }
    if body.KeepWeekly > 0 {
        args = append(args, "--keep-weekly", strconv.Itoa(body.KeepWeekly))
    }
    if body.KeepMonthly > 0 {
        args = append(args, "--keep-monthly", strconv.Itoa(body.KeepMonthly))
    }
    if body.KeepYearly > 0 {
        args = append(args, "--keep-yearly", strconv.Itoa(body.KeepYearly))
    }
    if strings.TrimSpace(body.KeepWithin) != "" {
        args = append(args, "--keep-within", body.KeepWithin)
    }

    cmd := exec.Command("restic", args...)
    cmd.Env = env
    out, err := cmd.CombinedOutput()
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "prune failed", "output": string(out)})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "prune completed", "output": string(out)})
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

// GET /api/servers/:server/backups/restic/repo/exists
func CheckServerResticRepo(c *gin.Context) {
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

    count := 0
    for _, entry := range entries {
        name := entry.Name()
        if name == serverId || strings.HasPrefix(name, serverId+"+") {
            count++
        }
    }

    c.JSON(http.StatusOK, gin.H{"exists": count > 0, "count": count})
}
