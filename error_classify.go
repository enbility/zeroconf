package zeroconf

import (
	"errors"
	"strings"
	"syscall"
)

// Windows socket error codes (not in standard syscall package).
// These constants are safe to define cross-platform because errors.Is()
// performs type comparison - on non-Windows systems, these simply won't match.
const (
	WSAENETDOWN      syscall.Errno = 10050 // Network is down
	WSAEADDRNOTAVAIL syscall.Errno = 10049 // Cannot assign requested address
	WSAEINVAL        syscall.Errno = 10022 // Invalid argument
)

// interfaceGoneErrors lists all error codes that indicate an interface is gone.
var interfaceGoneErrors = []syscall.Errno{
	// Unix errors
	syscall.ENXIO,         // "no such device or address"
	syscall.ENODEV,        // "no such device"
	syscall.EADDRNOTAVAIL, // "can't assign requested address"
	syscall.EINVAL,        // "invalid argument" (stale ifIndex)
	syscall.ENETDOWN,      // "network is down"
	syscall.ENETUNREACH,   // "network unreachable"
	// Windows errors
	WSAENETDOWN,
	WSAEADDRNOTAVAIL,
	WSAEINVAL,
}

// isInterfaceGone returns true if the error indicates the interface
// is no longer available and should be removed from active set.
//
// Uses errors.Is() for proper unwrapping of fmt.Errorf("%w") chains.
func isInterfaceGone(err error) bool {
	if err == nil {
		return false
	}

	// Check known error codes
	for _, e := range interfaceGoneErrors {
		if errors.Is(err, e) {
			return true
		}
	}

	// Fallback: check error message patterns for unknown error codes.
	// This handles edge cases on unusual platforms or new OS versions.
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "no such device") ||
		strings.Contains(errStr, "no such interface") ||
		strings.Contains(errStr, "network is down") ||
		strings.Contains(errStr, "network is unreachable") {
		return true
	}

	return false
}
