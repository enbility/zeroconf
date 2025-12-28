package zeroconf

import (
	"net"
	"sync"
	"time"
)

// Backoff intervals for adaptive retry strategy.
// Fast first retry for user-initiated reconnects, then progressive delay.
const (
	backoffFirst  = 1 * time.Second  // First retry: fast for quick reconnects
	backoffSecond = 5 * time.Second  // Second retry: moderate delay
	backoffMax    = 30 * time.Second // Subsequent retries: avoid thrashing
)

// failureState tracks failure history for adaptive backoff.
type failureState struct {
	count   int       // Number of consecutive failures
	retryAt time.Time // Don't retry until this time
}

// InterfaceManager tracks active and failed interfaces for one IP version.
// Thread-safe. Create separate instances for IPv4 and IPv6.
//
// Concurrency model:
//   - ActiveIndices() returns a snapshot; iteration is lock-free
//   - MarkFailed() is idempotent; safe to call even if already removed
//   - Sync() runs periodically in background; updates are atomic
type InterfaceManager struct {
	mu        sync.RWMutex
	active    map[int]string           // ifIndex -> name (currently usable)
	failures  map[string]*failureState // name -> failure tracking (adaptive backoff)
	requested []string                 // Mode selector:
	//   nil     = dynamic mode (accept any multicast interface)
	//   non-nil = explicit mode (only names in this slice)
	// NOTE: Empty slice []string{} is treated as explicit mode
	// with NO allowed interfaces - almost certainly a bug.
	// Callers should pass nil for dynamic mode, not empty slice.
}

// NewInterfaceManager creates a manager with initial interfaces.
// If requested is nil, dynamic mode is used (accepts new interfaces).
// If requested is non-nil, only those interface names are ever used.
func NewInterfaceManager(initial []net.Interface, requested []string) *InterfaceManager {
	m := &InterfaceManager{
		active:    make(map[int]string, len(initial)),
		failures:  make(map[string]*failureState),
		requested: requested,
	}
	for _, iface := range initial {
		m.active[iface.Index] = iface.Name
	}
	return m
}

// ActiveIndices returns current active interface indices.
// Call this in send loops - never cache the result.
//
// The returned slice is a snapshot. The caller iterates over it while
// the sync goroutine may modify the active map. This is safe because:
//   - Sends to removed indices fail fast and call MarkFailed (idempotent)
//   - New indices are picked up on the next ActiveIndices() call
func (m *InterfaceManager) ActiveIndices() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]int, 0, len(m.active))
	for idx := range m.active {
		result = append(result, idx)
	}
	return result
}

// MarkFailed removes an interface from active set if error indicates it's gone.
// Uses adaptive backoff: first failure = 1s, second = 5s, third+ = 30s.
//
// This method is IDEMPOTENT: safe to call even if the interface was already
// removed by a concurrent Sync() call.
//
// Returns true if the error indicated the interface is gone.
func (m *InterfaceManager) MarkFailed(ifIndex int, err error) bool {
	if !isInterfaceGone(err) {
		return false // Transient error, don't remove
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get name from active map (if still present)
	name := m.active[ifIndex]
	if name == "" {
		// Already removed - this is the benign race case
		// We can't set backoff without knowing the name, but that's OK:
		// either Sync() already set it, or we don't have enough info.
		return true
	}

	// Remove from active (idempotent - no-op if not present)
	delete(m.active, ifIndex)

	// Update failure tracking with adaptive backoff
	m.recordFailure(name)

	return true
}

// recordFailure updates the failure state for an interface (must hold lock).
func (m *InterfaceManager) recordFailure(name string) {
	state := m.failures[name]
	if state == nil {
		state = &failureState{}
		m.failures[name] = state
	}
	state.count++

	// Adaptive backoff based on failure count
	var backoff time.Duration
	switch state.count {
	case 1:
		backoff = backoffFirst // 1s - fast retry for quick reconnects
	case 2:
		backoff = backoffSecond // 5s - moderate delay
	default:
		backoff = backoffMax // 30s - avoid thrashing
	}
	state.retryAt = time.Now().Add(backoff)
}

// Sync updates state based on currently available interfaces.
// Returns interfaces that were recovered and need JoinGroup calls.
//
// Handles:
//   - Disappeared interfaces (removes from active, sets backoff)
//   - Index changes (interface reconnects with different index)
//   - New interfaces in dynamic mode
//   - Recovery after backoff expires
func (m *InterfaceManager) Sync(current []net.Interface) []net.Interface {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	currentByName := make(map[string]net.Interface, len(current))
	for _, iface := range current {
		currentByName[iface.Name] = iface
	}

	// Step 1: Remove disappeared interfaces
	for idx, name := range m.active {
		if _, exists := currentByName[name]; !exists {
			delete(m.active, idx)
			m.recordFailure(name)
		}
	}

	// Step 2: Find interfaces to recover and clean up stale indices
	var recovered []net.Interface
	for _, iface := range current {
		if m.shouldRecover(iface, now) {
			// Clean up stale index before adding to recovered list
			m.cleanupStaleIndex(iface)
			recovered = append(recovered, iface)
		}
	}

	return recovered
}

// shouldRecover checks if an interface should be recovered (must hold lock).
// NOTE: This is a pure predicate - it does NOT mutate state.
// Use cleanupStaleIndex() separately to handle index changes.
func (m *InterfaceManager) shouldRecover(iface net.Interface, now time.Time) bool {
	// Check if already active with same index
	if existingName, ok := m.active[iface.Index]; ok && existingName == iface.Name {
		return false // Already active, nothing to do
	}

	// Check mode restrictions
	if !m.isAllowed(iface.Name) {
		return false
	}

	// Check backoff
	if state := m.failures[iface.Name]; state != nil && now.Before(state.retryAt) {
		return false
	}

	return true
}

// isAllowed checks if interface name is allowed by mode (must hold lock).
func (m *InterfaceManager) isAllowed(name string) bool {
	if m.requested == nil {
		return true // Dynamic mode: allow all
	}
	for _, allowed := range m.requested {
		if allowed == name {
			return true
		}
	}
	return false // Explicit mode: not in requested set
}

// cleanupStaleIndex removes old index mapping if interface reconnected with new index.
// Must hold lock. Call this before adding new mapping for recovered interfaces.
func (m *InterfaceManager) cleanupStaleIndex(iface net.Interface) {
	for idx, name := range m.active {
		if name == iface.Name && idx != iface.Index {
			delete(m.active, idx)
			return // Only one stale mapping possible per name
		}
	}
}

// Activate adds an interface to the active set.
// Called after successful JoinGroup. Clears failure history.
// Handles the case where interface reconnected with a different index.
func (m *InterfaceManager) Activate(iface net.Interface) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove stale index mapping if interface reconnected with new index
	m.cleanupStaleIndex(iface)

	m.active[iface.Index] = iface.Name
	delete(m.failures, iface.Name) // Clear failure history on success
}

// SetBackoff marks an interface as temporarily failed (e.g., JoinGroup failed).
// Increments the failure counter for adaptive backoff.
func (m *InterfaceManager) SetBackoff(ifName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.recordFailure(ifName)
}

// GetActiveInterfaces returns full interface objects for all active indices.
// Used for IP address collection (avoids race between ActiveIndices and lookup).
func (m *InterfaceManager) GetActiveInterfaces() []net.Interface {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]net.Interface, 0, len(m.active))
	for idx := range m.active {
		if iface, err := net.InterfaceByIndex(idx); err == nil {
			result = append(result, *iface)
		}
	}
	return result
}
