package node

import "syscall"

// fsSpace is the filesystem's total and free bytes; ok is false where the stats are not
// available (macOS builds, unusual mounts).
func fsSpace(dir string) (total, free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Blocks * uint64(st.Bsize)), int64(st.Bavail * uint64(st.Bsize)), true
}
