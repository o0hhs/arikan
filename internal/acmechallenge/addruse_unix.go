//go:build !windows

package acmechallenge

import (
	"errors"
	"syscall"
)

// addressInUse is whether a listen failed because the address is held by another program.
// On Unix the errno survives unwrapping, and the message says so as well for a wrapped error
// that lost it.
func addressInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || containsAddressInUse(err.Error())
}

// containsAddressInUse is the message Unix gives.
func containsAddressInUse(msg string) bool {
	return contains(msg, "address already in use")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
