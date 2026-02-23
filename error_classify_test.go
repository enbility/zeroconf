package zeroconf

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestIsInterfaceGone_Nil_ReturnsFalse(t *testing.T) {
	if isInterfaceGone(nil) {
		t.Error("expected false for nil error")
	}
}

func TestIsInterfaceGone_ENXIO_ReturnsTrue(t *testing.T) {
	err := syscall.ENXIO
	if !isInterfaceGone(err) {
		t.Errorf("expected true for ENXIO, got false")
	}
}

func TestIsInterfaceGone_ENODEV_ReturnsTrue(t *testing.T) {
	err := syscall.ENODEV
	if !isInterfaceGone(err) {
		t.Errorf("expected true for ENODEV, got false")
	}
}

func TestIsInterfaceGone_EADDRNOTAVAIL_ReturnsTrue(t *testing.T) {
	err := syscall.EADDRNOTAVAIL
	if !isInterfaceGone(err) {
		t.Errorf("expected true for EADDRNOTAVAIL, got false")
	}
}

func TestIsInterfaceGone_EINVAL_ReturnsTrue(t *testing.T) {
	err := syscall.EINVAL
	if !isInterfaceGone(err) {
		t.Errorf("expected true for EINVAL, got false")
	}
}

func TestIsInterfaceGone_ENETDOWN_ReturnsTrue(t *testing.T) {
	err := syscall.ENETDOWN
	if !isInterfaceGone(err) {
		t.Errorf("expected true for ENETDOWN, got false")
	}
}

func TestIsInterfaceGone_ENETUNREACH_ReturnsTrue(t *testing.T) {
	err := syscall.ENETUNREACH
	if !isInterfaceGone(err) {
		t.Errorf("expected true for ENETUNREACH, got false")
	}
}

func TestIsInterfaceGone_WrappedError_ReturnsTrue(t *testing.T) {
	// errors.Is() should unwrap fmt.Errorf("%w") chains
	wrapped := fmt.Errorf("send failed: %w", syscall.ENXIO)
	if !isInterfaceGone(wrapped) {
		t.Errorf("expected true for wrapped ENXIO, got false")
	}
}

func TestIsInterfaceGone_DoubleWrappedError_ReturnsTrue(t *testing.T) {
	inner := fmt.Errorf("inner: %w", syscall.ENETDOWN)
	outer := fmt.Errorf("outer: %w", inner)
	if !isInterfaceGone(outer) {
		t.Errorf("expected true for double-wrapped ENETDOWN, got false")
	}
}

func TestIsInterfaceGone_TransientError_ReturnsFalse(t *testing.T) {
	// EAGAIN is a transient error - should not remove interface
	err := syscall.EAGAIN
	if isInterfaceGone(err) {
		t.Errorf("expected false for EAGAIN (transient), got true")
	}
}

func TestIsInterfaceGone_ETIMEDOUT_ReturnsFalse(t *testing.T) {
	// Timeout is transient - interface might still be fine
	err := syscall.ETIMEDOUT
	if isInterfaceGone(err) {
		t.Errorf("expected false for ETIMEDOUT (transient), got true")
	}
}

func TestIsInterfaceGone_GenericError_ReturnsFalse(t *testing.T) {
	err := errors.New("some random error")
	if isInterfaceGone(err) {
		t.Errorf("expected false for generic error, got true")
	}
}

func TestIsInterfaceGone_FallbackMessageParsing_NoSuchDevice(t *testing.T) {
	// Fallback for unknown error codes with recognizable message
	err := errors.New("operation failed: no such device")
	if !isInterfaceGone(err) {
		t.Errorf("expected true for 'no such device' message, got false")
	}
}

func TestIsInterfaceGone_FallbackMessageParsing_NetworkDown(t *testing.T) {
	err := errors.New("send error: network is down")
	if !isInterfaceGone(err) {
		t.Errorf("expected true for 'network is down' message, got false")
	}
}

func TestIsInterfaceGone_FallbackMessageParsing_NetworkUnreachable(t *testing.T) {
	err := errors.New("cannot route: network is unreachable")
	if !isInterfaceGone(err) {
		t.Errorf("expected true for 'network is unreachable' message, got false")
	}
}

func TestIsInterfaceGone_FallbackMessageParsing_NoSuchInterface(t *testing.T) {
	err := errors.New("interface eth0: no such interface")
	if !isInterfaceGone(err) {
		t.Errorf("expected true for 'no such interface' message, got false")
	}
}

func TestIsInterfaceGone_FallbackMessageParsing_CaseInsensitive(t *testing.T) {
	err := errors.New("NETWORK IS DOWN")
	if !isInterfaceGone(err) {
		t.Errorf("expected true for uppercase 'NETWORK IS DOWN', got false")
	}
}
