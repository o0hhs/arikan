package node

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mikan/internal/nodeapi"
)

// The node's disk, for the panel: what the data directory's filesystem has free, what its
// own directories take, and what a cleanup removes. The panel shows it on the Nodes page
// and runs the cleanup with one button. What grows on a node is the update directory
// (leftovers of interrupted updates and of older updaters) and files atomic writes left
// when the process died between write and rename; nothing else in the data directory is
// touched.

// junkSuffixes are names a cleanup may remove anywhere in the data directory.
var junkSuffixes = []string{".tmp", ".old", "~"}

// updateKeep are the files of the update directory that are its working state, not junk.
var updateKeep = map[string]bool{"request": true, "status.json": true}

func isJunkName(name string, inUpdate bool) bool {
	if inUpdate && !updateKeep[name] {
		return true
	}
	for _, s := range junkSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// diskScan is one look at the data directory.
type diskScan struct {
	total, free, used int64
	dirs              []nodeapi.DirUsage
	junk              int64
	files             []string
}

// scanDisk measures the data directory: its filesystem, its own subdirectories, and the
// junk Clean removes. It follows no symlinks and skips nothing else: the directory is the
// node's own, small and trusted.
func scanDisk(dataDir string, log *slog.Logger) diskScan {
	var s diskScan
	s.total, s.free, _ = fsSpace(dataDir)
	if s.total > 0 {
		s.used = s.total - s.free
	}
	sizes := map[string]int64{}
	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // a vanished entry is no news
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() <= 0 {
			return nil
		}
		if head, _, ok := strings.Cut(rel, string(filepath.Separator)); ok {
			sizes[head] += info.Size()
		}
		inUpdate := strings.HasPrefix(rel, updateDir+string(filepath.Separator))
		if isJunkName(d.Name(), inUpdate) {
			s.junk += info.Size()
			s.files = append(s.files, rel)
		}
		return nil
	})
	for name, bytes := range sizes {
		s.dirs = append(s.dirs, nodeapi.DirUsage{Path: name, Bytes: bytes})
	}
	sort.Slice(s.dirs, func(i, j int) bool { return s.dirs[i].Bytes > s.dirs[j].Bytes })
	sort.Strings(s.files)
	return s
}

// CleanDisk removes the data directory's junk and reports what the disk looks like after.
// Removing one file fails on its own, not the whole cleanup.
func (e *Engine) CleanDisk() nodeapi.DiskStatus {
	scan := scanDisk(e.dataDir, e.log)
	freed := int64(0)
	for _, rel := range scan.files {
		path := filepath.Join(e.dataDir, rel)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			if err == nil {
				e.log.Warn("disk clean: not a regular file", "file", rel)
			}
			continue
		}
		if err := os.Remove(path); err != nil {
			e.log.Warn("disk clean", "file", rel, "err", err)
			continue
		}
		freed += info.Size()
	}
	if freed > 0 {
		e.log.Info("disk clean", "freed", freedString(freed))
	}
	after := scanDisk(e.dataDir, e.log)
	return nodeapi.DiskStatus{
		Total: after.total, Free: after.free, Used: after.used,
		Dirs: after.dirs, Junk: after.junk, Files: after.files, Freed: freed,
	}
}

// DiskStatus is the data directory's disk as it is now.
func (e *Engine) DiskStatus() nodeapi.DiskStatus {
	s := scanDisk(e.dataDir, e.log)
	return nodeapi.DiskStatus{
		Total: s.total, Free: s.free, Used: s.used,
		Dirs: s.dirs, Junk: s.junk, Files: s.files,
	}
}

// freedString is the freed bytes in human terms, for the log.
func freedString(freed int64) string {
	if freed >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(freed)/(1<<30))
	}
	if freed >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(freed)/(1<<20))
	}
	return fmt.Sprintf("%d B", freed)
}
