package zeroconf

import (
	"net"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Helper to create mock interfaces
func mockInterface(index int, name string) net.Interface {
	return net.Interface{
		Index: index,
		Name:  name,
		Flags: net.FlagUp | net.FlagMulticast,
	}
}

func mockInterfaces(specs ...struct{ idx int; name string }) []net.Interface {
	result := make([]net.Interface, len(specs))
	for i, s := range specs {
		result[i] = mockInterface(s.idx, s.name)
	}
	return result
}

// ============================================================================
// NewInterfaceManager Tests
// ============================================================================

func TestInterfaceManager_NewDynamicMode(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}

	// nil requested = dynamic mode
	mgr := NewInterfaceManager(ifaces, nil)

	indices := mgr.ActiveIndices()
	if len(indices) != 2 {
		t.Errorf("expected 2 active indices, got %d", len(indices))
	}
}

func TestInterfaceManager_NewExplicitMode(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}
	requested := []string{"eth0", "wlan0"}

	mgr := NewInterfaceManager(ifaces, requested)

	indices := mgr.ActiveIndices()
	if len(indices) != 2 {
		t.Errorf("expected 2 active indices, got %d", len(indices))
	}
}

func TestInterfaceManager_NewEmptyInitial(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	indices := mgr.ActiveIndices()
	if len(indices) != 0 {
		t.Errorf("expected 0 active indices, got %d", len(indices))
	}
}

// ============================================================================
// ActiveIndices Tests
// ============================================================================

func TestInterfaceManager_ActiveIndices_ReturnsSnapshot(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(5, "wlan0")}
	mgr := NewInterfaceManager(ifaces, nil)

	indices := mgr.ActiveIndices()

	// Should contain both indices
	sort.Ints(indices)
	if len(indices) != 2 || indices[0] != 1 || indices[1] != 5 {
		t.Errorf("expected [1, 5], got %v", indices)
	}
}

func TestInterfaceManager_ActiveIndices_ReturnsEmptySliceNotNil(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	indices := mgr.ActiveIndices()

	if indices == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(indices) != 0 {
		t.Errorf("expected length 0, got %d", len(indices))
	}
}

// ============================================================================
// MarkFailed Tests
// ============================================================================

func TestInterfaceManager_MarkFailed_InterfaceGoneError_RemovesInterface(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// ENXIO indicates interface is gone
	removed := mgr.MarkFailed(1, syscall.ENXIO)

	if !removed {
		t.Error("expected MarkFailed to return true for ENXIO")
	}

	indices := mgr.ActiveIndices()
	if len(indices) != 1 {
		t.Errorf("expected 1 active index after removal, got %d", len(indices))
	}
	if indices[0] != 2 {
		t.Errorf("expected remaining index to be 2, got %d", indices[0])
	}
}

func TestInterfaceManager_MarkFailed_TransientError_KeepsInterface(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// EAGAIN is transient - should not remove
	removed := mgr.MarkFailed(1, syscall.EAGAIN)

	if removed {
		t.Error("expected MarkFailed to return false for transient error")
	}

	indices := mgr.ActiveIndices()
	if len(indices) != 1 {
		t.Errorf("expected interface to remain active, got %d active", len(indices))
	}
}

func TestInterfaceManager_MarkFailed_Idempotent_SafeWhenAlreadyRemoved(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// First removal
	mgr.MarkFailed(1, syscall.ENXIO)

	// Second removal of same index - should not panic
	removed := mgr.MarkFailed(1, syscall.ENXIO)

	// Returns true because error still indicates "interface gone"
	if !removed {
		t.Error("expected MarkFailed to return true even when already removed")
	}

	indices := mgr.ActiveIndices()
	if len(indices) != 0 {
		t.Errorf("expected 0 active indices, got %d", len(indices))
	}
}

func TestInterfaceManager_MarkFailed_UnknownIndex_DoesNotPanic(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	// Index 999 was never added - should not panic
	removed := mgr.MarkFailed(999, syscall.ENXIO)

	if !removed {
		t.Error("expected true because error indicates interface gone")
	}
}

// ============================================================================
// Adaptive Backoff Tests
// ============================================================================

func TestInterfaceManager_AdaptiveBackoff_FirstFailure1s(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// Fail the interface
	mgr.MarkFailed(1, syscall.ENXIO)

	// Check backoff is ~1s
	mgr.mu.RLock()
	state := mgr.failures["eth0"]
	mgr.mu.RUnlock()

	if state == nil {
		t.Fatal("expected failure state to exist")
	}
	if state.count != 1 {
		t.Errorf("expected count 1, got %d", state.count)
	}

	// retryAt should be ~1s from now
	expectedBackoff := backoffFirst
	actualBackoff := time.Until(state.retryAt)
	if actualBackoff < expectedBackoff-100*time.Millisecond || actualBackoff > expectedBackoff+100*time.Millisecond {
		t.Errorf("expected backoff ~%v, got %v", expectedBackoff, actualBackoff)
	}
}

func TestInterfaceManager_AdaptiveBackoff_SecondFailure5s(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// First failure
	mgr.MarkFailed(1, syscall.ENXIO)

	// Manually re-add and fail again (simulating Sync + re-fail)
	mgr.mu.Lock()
	mgr.active[1] = "eth0"
	mgr.mu.Unlock()
	mgr.MarkFailed(1, syscall.ENXIO)

	mgr.mu.RLock()
	state := mgr.failures["eth0"]
	mgr.mu.RUnlock()

	if state.count != 2 {
		t.Errorf("expected count 2, got %d", state.count)
	}

	expectedBackoff := backoffSecond
	actualBackoff := time.Until(state.retryAt)
	if actualBackoff < expectedBackoff-100*time.Millisecond || actualBackoff > expectedBackoff+100*time.Millisecond {
		t.Errorf("expected backoff ~%v, got %v", expectedBackoff, actualBackoff)
	}
}

func TestInterfaceManager_AdaptiveBackoff_ThirdFailure30s(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// Three failures
	for i := 0; i < 3; i++ {
		mgr.mu.Lock()
		mgr.active[1] = "eth0"
		mgr.mu.Unlock()
		mgr.MarkFailed(1, syscall.ENXIO)
	}

	mgr.mu.RLock()
	state := mgr.failures["eth0"]
	mgr.mu.RUnlock()

	if state.count != 3 {
		t.Errorf("expected count 3, got %d", state.count)
	}

	expectedBackoff := backoffMax
	actualBackoff := time.Until(state.retryAt)
	if actualBackoff < expectedBackoff-100*time.Millisecond || actualBackoff > expectedBackoff+100*time.Millisecond {
		t.Errorf("expected backoff ~%v, got %v", expectedBackoff, actualBackoff)
	}
}

// ============================================================================
// Sync Tests
// ============================================================================

func TestInterfaceManager_Sync_DetectsDisappeared(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// wlan0 disappeared
	current := []net.Interface{mockInterface(1, "eth0")}

	recovered := mgr.Sync(current)

	// Nothing to recover (eth0 was already active)
	if len(recovered) != 0 {
		t.Errorf("expected 0 recovered, got %d", len(recovered))
	}

	// wlan0 should be removed
	indices := mgr.ActiveIndices()
	if len(indices) != 1 || indices[0] != 1 {
		t.Errorf("expected [1], got %v", indices)
	}

	// wlan0 should have failure state
	mgr.mu.RLock()
	_, hasFailure := mgr.failures["wlan0"]
	mgr.mu.RUnlock()
	if !hasFailure {
		t.Error("expected failure state for wlan0")
	}
}

func TestInterfaceManager_Sync_RecoversAfterBackoff(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// Remove eth0 and set backoff in the past
	mgr.mu.Lock()
	delete(mgr.active, 1)
	mgr.failures["eth0"] = &failureState{
		count:   1,
		retryAt: time.Now().Add(-1 * time.Second), // Backoff expired
	}
	mgr.mu.Unlock()

	// eth0 reappears
	current := []net.Interface{mockInterface(1, "eth0")}

	recovered := mgr.Sync(current)

	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered, got %d", len(recovered))
	}
	if recovered[0].Name != "eth0" {
		t.Errorf("expected eth0 to be recovered, got %s", recovered[0].Name)
	}
}

func TestInterfaceManager_Sync_RespectsBackoffNotExpired(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// Remove eth0 and set backoff in the future
	mgr.mu.Lock()
	delete(mgr.active, 1)
	mgr.failures["eth0"] = &failureState{
		count:   1,
		retryAt: time.Now().Add(10 * time.Second), // Backoff NOT expired
	}
	mgr.mu.Unlock()

	current := []net.Interface{mockInterface(1, "eth0")}

	recovered := mgr.Sync(current)

	// Should NOT recover yet
	if len(recovered) != 0 {
		t.Errorf("expected 0 recovered (backoff not expired), got %d", len(recovered))
	}
}

func TestInterfaceManager_Sync_RespectsExplicitMode(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	requested := []string{"eth0"} // Only eth0 allowed
	mgr := NewInterfaceManager(ifaces, requested)

	// New interface wlan0 appears (not in requested list)
	current := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}

	recovered := mgr.Sync(current)

	// wlan0 should NOT be recovered (not in requested)
	for _, iface := range recovered {
		if iface.Name == "wlan0" {
			t.Error("wlan0 should not be recovered in explicit mode")
		}
	}

	// Only eth0 should be active
	indices := mgr.ActiveIndices()
	if len(indices) != 1 || indices[0] != 1 {
		t.Errorf("expected [1], got %v", indices)
	}
}

func TestInterfaceManager_Sync_AcceptsNewInDynamicMode(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil) // nil = dynamic mode

	// New interface wlan0 appears
	current := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}

	recovered := mgr.Sync(current)

	// wlan0 should be in recovered list
	found := false
	for _, iface := range recovered {
		if iface.Name == "wlan0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected wlan0 to be in recovered list (dynamic mode)")
	}
}

func TestInterfaceManager_Sync_DetectsIndexChange(t *testing.T) {
	// eth0 starts with index 1
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// eth0 reconnects with index 5 (different index, same name)
	current := []net.Interface{mockInterface(5, "eth0")}

	recovered := mgr.Sync(current)

	// eth0 should be recovered with new index
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered, got %d", len(recovered))
	}
	if recovered[0].Index != 5 || recovered[0].Name != "eth0" {
		t.Errorf("expected {5, eth0}, got {%d, %s}", recovered[0].Index, recovered[0].Name)
	}

	// Old index 1 should be removed
	mgr.mu.RLock()
	_, hasOld := mgr.active[1]
	mgr.mu.RUnlock()
	if hasOld {
		t.Error("old index 1 should be removed")
	}
}

// ============================================================================
// Activate Tests
// ============================================================================

func TestInterfaceManager_Activate_AddsToActive(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	iface := mockInterface(3, "eth1")
	mgr.Activate(iface)

	indices := mgr.ActiveIndices()
	if len(indices) != 1 || indices[0] != 3 {
		t.Errorf("expected [3], got %v", indices)
	}
}

func TestInterfaceManager_Activate_ClearsFailureHistory(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	// Set up failure state
	mgr.mu.Lock()
	mgr.failures["eth1"] = &failureState{count: 5, retryAt: time.Now().Add(time.Hour)}
	mgr.mu.Unlock()

	// Activate should clear it
	iface := mockInterface(3, "eth1")
	mgr.Activate(iface)

	mgr.mu.RLock()
	_, hasFailure := mgr.failures["eth1"]
	mgr.mu.RUnlock()

	if hasFailure {
		t.Error("expected failure history to be cleared after Activate")
	}
}

func TestInterfaceManager_Activate_HandlesIndexChange(t *testing.T) {
	// Start with eth0 at index 1
	ifaces := []net.Interface{mockInterface(1, "eth0")}
	mgr := NewInterfaceManager(ifaces, nil)

	// Activate eth0 with new index 5
	mgr.Activate(mockInterface(5, "eth0"))

	indices := mgr.ActiveIndices()
	sort.Ints(indices)

	// Should only have index 5, not both 1 and 5
	if len(indices) != 1 || indices[0] != 5 {
		t.Errorf("expected [5], got %v", indices)
	}
}

// ============================================================================
// SetBackoff Tests
// ============================================================================

func TestInterfaceManager_SetBackoff_SetsFailureState(t *testing.T) {
	mgr := NewInterfaceManager(nil, nil)

	mgr.SetBackoff("eth0")

	mgr.mu.RLock()
	state := mgr.failures["eth0"]
	mgr.mu.RUnlock()

	if state == nil {
		t.Fatal("expected failure state to be set")
	}
	if state.count != 1 {
		t.Errorf("expected count 1, got %d", state.count)
	}
}

// ============================================================================
// GetActiveInterfaces Tests
// ============================================================================

func TestInterfaceManager_GetActiveInterfaces_ReturnsInterfaces(t *testing.T) {
	// This test requires actual system interfaces, so we'll just test the empty case
	mgr := NewInterfaceManager(nil, nil)

	ifaces := mgr.GetActiveInterfaces()

	if ifaces == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(ifaces) != 0 {
		t.Errorf("expected 0 interfaces, got %d", len(ifaces))
	}
}

// ============================================================================
// Concurrency Tests
// ============================================================================

func TestInterfaceManager_Concurrent_ReadWrite(t *testing.T) {
	ifaces := []net.Interface{mockInterface(1, "eth0"), mockInterface(2, "wlan0")}
	mgr := NewInterfaceManager(ifaces, nil)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.ActiveIndices()
			}
		}
	}()

	// Writer goroutine - MarkFailed
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-stop:
				return
			default:
				mgr.MarkFailed(1, syscall.ENXIO)
			}
		}
	}()

	// Writer goroutine - Sync
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-stop:
				return
			default:
				mgr.Sync(ifaces)
			}
		}
	}()

	// Writer goroutine - Activate
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-stop:
				return
			default:
				mgr.Activate(mockInterface(3, "eth1"))
			}
		}
	}()

	// Let them run for a bit
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// If we get here without deadlock or panic, the test passes
}
