//go:build !linux

package node

// fsSpace is not available off Linux: the node's servers are Linux (the tests build and
// run on any platform, for the junk scan alone).
func fsSpace(string) (total, free int64, ok bool) { return 0, 0, false }
