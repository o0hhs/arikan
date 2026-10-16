//go:build windows

package acmechallenge

import (
	"errors"
	"os"
	"syscall"
)

// wsaEADDRINUSE is Winsock's "address already in use". Go's syscall.EADDRINUSE is a Unix
// value that a Windows listen error does not unwrap to, so the Winsock number is compared
// directly.
const wsaEADDRINUSE = syscall.Errno(10048)

// addressInUse is whether a listen failed because the address is held by another program.
func addressInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, wsaEADDRINUSE) {
		return true
	}
	var se *os.SyscallError
	if errors.As(err, &se) && errors.Is(se.Err, wsaEADDRINUSE) {
		return true
	}
	// Windows words it "Only one usage of each socket address ... is normally permitted".
	return contains(msgOf(err), "Only one usage of each socket address")
}

func msgOf(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
