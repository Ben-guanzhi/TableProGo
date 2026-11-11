package main

import (
	"testing"
	"time"

	"github.com/dbcore/dbcore/schemadiff"
)

// TestReclaimerSetActiveRelease tests the active/pending accounting. The
// worker is not started so the accounting state stays stable.
func TestReclaimerSetActiveRelease(t *testing.T) {
	r := NewReclaimer()

	// Register an active document.
	r.SetActive(&struct{}{}, true, 20<<20, true)

	// shouldCollect returns false when pending is below threshold.
	r.mu.Lock()
	if r.shouldCollect() {
		t.Error("shouldCollect returned true with no pending bytes")
	}
	r.mu.Unlock()

	// Release enough bytes to trigger collection.
	r.Release(64 << 20)

	r.mu.Lock()
	if !r.shouldCollect() {
		t.Error("shouldCollect returned false after releasing 64MB")
	}
	if r.pendingBytes != 64<<20 {
		t.Errorf("pendingBytes = %d, want %d", r.pendingBytes, 64<<20)
	}
	r.mu.Unlock()
}

// TestReclaimerActiveBytesGate tests that an active document that doesn't
// allow idle collection suppresses collection.
func TestReclaimerActiveBytesGate(t *testing.T) {
	r := NewReclaimer()

	// Register an active document that blocks idle collection.
	r.SetActive(&struct{}{}, true, 50<<20, false) // allowsIdleCollection = false

	r.Release(64 << 20)

	r.mu.Lock()
	if r.shouldCollect() {
		t.Error("shouldCollect returned true with an active doc that blocks idle collection")
	}
	r.mu.Unlock()
}

// TestReclaimerActiveBytesOverflow tests that total active bytes above the
// threshold suppresses collection.
func TestReclaimerActiveBytesOverflow(t *testing.T) {
	r := NewReclaimer()

	// Two active documents, each 50MB, total 100MB > 96MB threshold.
	// Use new(int) (non-zero-size) so the keys are distinct — Go reuses the
	// address of zero-sized allocations like &struct{}{}.
	r.SetActive(new(int), true, 50<<20, true)
	r.SetActive(new(int), true, 50<<20, true)

	r.Release(64 << 20)

	r.mu.Lock()
	if r.shouldCollect() {
		t.Error("shouldCollect returned true with 100MB active (threshold 96MB)")
	}
	r.mu.Unlock()
}

// TestReclaimerActiveDocumentUnregister tests that unregistering an active
// document removes it from the accounting.
func TestReclaimerActiveDocumentUnregister(t *testing.T) {
	r := NewReclaimer()
	doc := &struct{}{}
	r.SetActive(doc, true, 50<<20, true)

	// Release 64MB — but 50MB is still active, total 50MB < 96MB, so collection
	// should be allowed.
	r.Release(64 << 20)
	r.mu.Lock()
	if !r.shouldCollect() {
		t.Error("shouldCollect returned false with 50MB active and 64MB pending")
	}
	r.mu.Unlock()

	// Unregister the active document.
	r.SetActive(doc, false, 0, true)

	// Now collection should be allowed again (0 active bytes).
	r.mu.Lock()
	if !r.shouldCollect() {
		t.Error("shouldCollect returned false after unregistering the active doc")
	}
	r.mu.Unlock()
}

// TestReclaimerRateLimit tests that the minimum interval between collections
// is respected.
func TestReclaimerRateLimit(t *testing.T) {
	r := NewReclaimer()
	r.Start()
	defer r.Stop()

	r.SetActive(&struct{}{}, true, 0, true)
	r.Release(64 << 20)

	// The worker should have collected after the quiescence delay.
	// Check that lastGC was set (meaning the worker ran).
	deadline := time.Now().Add(reclaimQuiescenceDelay + 2*time.Second)
	for {
		r.mu.Lock()
		collected := !r.lastGC.IsZero()
		r.mu.Unlock()
		if collected {
			break
		}
		if time.Now().After(deadline) {
			t.Error("worker did not collect within the expected window")
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestReclaimerComparePlanLifecycle tests the integration with compare plans:
// creating a plan registers it as active, and filling the cache evicts and
// releases older plans.
func TestReclaimerComparePlanLifecycle(t *testing.T) {
	a := NewApp()
	defer a.Shutdown(nil)

	if a.reclaimer == nil {
		t.Fatal("App does not have a reclaimer")
	}

	// Create a compare plan with enough statements to exceed the eligibility
	// threshold (1MB). Each statement is ~512 bytes, so we need ~2100+.
	statements := make([]schemadiff.Statement, 2200)

	// Store one plan: it should be registered as active.
	a.storeComparePlan(CompareTarget{}, "sync", "public", statements)

	r := a.reclaimer
	r.mu.Lock()
	activeCount := len(r.active)
	r.mu.Unlock()
	if activeCount == 0 {
		t.Error("reclaimer did not register the compare plan as active")
	}

	// Fill the plan cache to maxComparePlans. This should trigger evictions,
	// which deregister the evicted plans from the reclaimer.
	for i := 0; i < maxComparePlans; i++ {
		a.storeComparePlan(CompareTarget{}, "sync", "public", statements)
	}

	// After filling the cache, the comparePlans map should have exactly
	// maxComparePlans entries.
	a.compareMu.Lock()
	planCount := len(a.comparePlans)
	a.compareMu.Unlock()
	if planCount != maxComparePlans {
		t.Errorf("comparePlans has %d entries after filling cache, want %d",
			planCount, maxComparePlans)
	}
}
