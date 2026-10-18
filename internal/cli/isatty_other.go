//go:build !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !linux && !aix && !zos && !windows

package cli

func isTerminal(fd uintptr) bool {
	return false
}
