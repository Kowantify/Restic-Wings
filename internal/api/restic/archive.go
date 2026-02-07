package restic

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// Archived repos are created when a server is deleted: the repo folder is moved into this directory.
// This API is intended for panel-admin tooling (browse/download/delete).
const resticArchiveBaseDir = "/var/lib/pterodactyl/restic/archive"

var archiveIdRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]{0,254}$`)

type archiveItem struct {
	ID         string `json:"id"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

func safeArchivePath(id string) (string, bool) {
	if !archiveIdRe.MatchString(id) {
		return "", false
	}
	base := filepath.Clean(resticArchiveBaseDir)
	target := filepath.Clean(filepath.Join(base, id))

	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", false
	}
	// Rel must not escape (e.g. "../..") and must not be "." (the base itself).
	if rel == "." || rel == "" || rel == ".." || stringsHasDotDot(rel) {
		return "", false
	}

	return target, true
}

func stringsHasDotDot(rel string) bool {
	// filepath.Rel returns paths using the OS separator.
	// Any ".." path segment indicates traversal outside base.
	parts := splitPath(rel)
	for _, p := range parts {
		if p == ".." {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(p); i++ {
		if os.IsPathSeparator(p[i]) {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(p[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func dirSizeBytes(root string) (int64, error) {
	var total int64 = 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// ListArchivedRepos returns archived repo folder names in resticArchiveBaseDir.
func ListArchivedRepos(c *gin.Context) {
	entries, err := os.ReadDir(resticArchiveBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"archives": []archiveItem{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read archive directory."})
		return
	}

	items := make([]archiveItem, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !archiveIdRe.MatchString(id) {
			continue
		}
		full, ok := safeArchivePath(id)
		if !ok {
			continue
		}
		info, _ := e.Info()
		var mtime time.Time
		if info != nil {
			mtime = info.ModTime()
		}
		size, _ := dirSizeBytes(full)
		item := archiveItem{
			ID:        id,
			SizeBytes: size,
		}
		if !mtime.IsZero() {
			item.ModifiedAt = mtime.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].ModifiedAt > items[j].ModifiedAt })
	c.JSON(http.StatusOK, gin.H{"archives": items})
}

// DeleteArchivedRepo deletes an archived repo folder by id.
func DeleteArchivedRepo(c *gin.Context) {
	id := c.Param("archiveId")
	target, ok := safeArchivePath(id)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid archive id."})
		return
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Archive not found."})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to access archive."})
		return
	}
	if err := os.RemoveAll(target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete archive."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func writeTarGz(w io.Writer, dir string, baseName string) error {
	gw := gzip.NewWriter(w)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		name := filepath.ToSlash(filepath.Join(baseName, rel))

		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			// Best-effort: record the link target in the archive header.
			if t, e := os.Readlink(p); e == nil {
				link = t
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// DownloadArchivedRepo streams a tar.gz of an archived repo folder.
func DownloadArchivedRepo(c *gin.Context) {
	id := c.Param("archiveId")
	target, ok := safeArchivePath(id)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid archive id."})
		return
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		if err != nil && os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Archive not found."})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Archive not found."})
		return
	}

	filename := "restic-archive-" + id + ".tar.gz"
	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Header("Cache-Control", "no-store")

	c.Status(http.StatusOK)
	_ = writeTarGz(c.Writer, target, id)
}

