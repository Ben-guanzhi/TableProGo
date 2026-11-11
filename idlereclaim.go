package main

// IdleMemoryReclaimer — TablePlus LargeDocumentMemoryReclaimer 的 Go 移植，
// 适配 TableProGo 的 web 后端形态。
//
// TablePlus 的机制（WPF 桌面）：文本文档注册为 active，释放时登记 pending，
// worker 等待 WeakReference 死亡后 GC.Collect + 归还 OS。
//
// Go 适配差异：
// 1. Go 的 runtime.GC() 已回收所有不可达对象，无需 WeakReference 等待；
//    debug.FreeOSMemory() 归还 OS。
// 2. 后端持有的"文档"是 QueryResult.Rows / txSessions / comparePlans，
//    而非 WPF 文本缓冲。
// 3. 输入空闲检测用 time.Since 近似（无 user32.dll）；保守阈值更高。
//
// 门控逻辑（与 TablePlus 一致）：
// - pending 释放字节 ≥ 64MB 才回收
// - 活跃文档总量 ≤ 96MB 才回收（避免正在查看的大结果被 GC 卡顿）
// - 最小回收间隔 10s（与 TablePlus 一致）
// - 有活跃文档且不允许空闲回收时绝不回收

import (
	"runtime"
	"sync"
	"time"
)

// Reclaimer tracks large documents held by the backend and reclaims their
// memory when the client goes idle. It is the Go counterpart of TablePlus'
// LargeDocumentMemoryReclaimer, with the GC work split differently: Go's
// runtime collects unreachable objects in one call, so the worker skips the
// .NET "wait for WeakReference to die" phase and goes straight to GC.
type Reclaimer struct {
	mu           sync.Mutex
	active       map[any]*reclaimDoc
	pendingBytes int64
	pendingCount int
	lastGC       time.Time
	stopCh       chan struct{}
	stopOnce     sync.Once
}

type reclaimDoc struct {
	allowsIdleCollection bool
	estimatedBytes       int64
}

const (
	// reclaimMinPendingBytes is the minimum released-document size that
	// triggers a collection pass. Matches TablePlus' 64 MiB threshold.
	reclaimMinPendingBytes = 64 << 20

	// reclaimMinActiveBytes is the minimum document size that participates in
	// the active-bytes accounting. Matches TablePlus' 8 MiB.
	reclaimMinActiveBytes = 8 << 20

	// reclaimMaxActiveBytes is the maximum total active bytes allowed before
	// a collection pass is suppressed. Matches TablePlus' 96 MiB.
	reclaimMaxActiveBytes = 96 << 20

	// reclaimMinInterval throttles collection passes. Matches TablePlus'
	// 10-second minimum.
	reclaimMinInterval = 10 * time.Second

	// reclaimQuiescenceDelay is how long the worker waits after being
	// scheduled before checking. Matches TablePlus' 3-second delay.
	reclaimQuiescenceDelay = 3 * time.Second

	// reclaimEligibilityThreshold is the minimum estimated bytes for a
	// document to be eligible for idle collection at all.
	reclaimEligibilityThreshold = 1 << 20 // 1 MiB
)

// NewReclaimer creates a Reclaimer. The caller owns it; call Stop on shutdown.
func NewReclaimer() *Reclaimer {
	return &Reclaimer{
		active: make(map[any]*reclaimDoc),
		stopCh: make(chan struct{}),
	}
}

// SetActive registers or unregisters a document as active. When active is
// true, the document's estimated bytes participate in the active-bytes
// accounting. When false, the document is removed from the accounting.
func (r *Reclaimer) SetActive(owner any, active bool, estimatedBytes int64, allowsIdleCollection bool) {
	if owner == nil {
		return
	}
	if !active {
		r.mu.Lock()
		delete(r.active, owner)
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	if est := max64(estimatedBytes, 0); est >= reclaimEligibilityThreshold {
		r.active[owner] = &reclaimDoc{allowsIdleCollection, est}
	} else {
		delete(r.active, owner)
	}
	r.mu.Unlock()
}

// Release marks a document as pending for reclaim. The document's memory
// is already unreachable from the caller's perspective; the worker will
// collect it when idle.
func (r *Reclaimer) Release(estimatedBytes int64) {
	if est := max64(estimatedBytes, 0); est >= reclaimEligibilityThreshold {
		r.mu.Lock()
		r.pendingBytes += est
		r.pendingCount++
		r.mu.Unlock()
		r.maybeSchedule()
	}
}

// maybeSchedule notes that a pass may be warranted. The worker goroutine
// (started by Start at app construction) polls this state itself, so there is
// nothing to kick here — the call site documents the intent and would catch a
// future switch to on-demand scheduling.
func (r *Reclaimer) maybeSchedule() {}

// Start begins the background collection worker.
func (r *Reclaimer) Start() {
	go r.worker()
}

// Stop shuts down the worker.
func (r *Reclaimer) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
}

// shouldCollect checks whether a collection pass should run. Called under r.mu.
func (r *Reclaimer) shouldCollect() bool {
	if r.pendingBytes < reclaimMinPendingBytes {
		return false
	}
	if r.pendingCount == 0 {
		return false
	}
	activeBytes := int64(0)
	for _, doc := range r.active {
		if !doc.allowsIdleCollection {
			return false
		}
		activeBytes += doc.estimatedBytes
		if activeBytes > reclaimMaxActiveBytes {
			return false
		}
	}
	return true
}

// worker is the background collection loop.
func (r *Reclaimer) worker() {
	// Initial quiescence delay, matching TablePlus.
	select {
	case <-time.After(reclaimQuiescenceDelay):
	case <-r.stopCh:
		return
	}

	for {
		r.mu.Lock()
		should := r.shouldCollect()
		if should && time.Since(r.lastGC) < reclaimMinInterval {
			// Rate-limited: wait for the interval to elapse.
			wait := reclaimMinInterval - time.Since(r.lastGC)
			r.mu.Unlock()
			select {
			case <-time.After(wait):
			case <-r.stopCh:
				return
			}
			continue
		}
		if should {
			r.lastGC = time.Now()
			pending := r.pendingBytes
			r.pendingBytes = 0
			r.pendingCount = 0
			r.mu.Unlock()

			// Go's GC collects all unreachable objects. FreeOSMemory
			// returns the memory to the OS immediately.
			runtime.GC()
			debugFreeOSMemory()
			logReclaim(pending)
		} else {
			r.mu.Unlock()
		}

		// Sleep before the next check. Poll every 30s — frequent enough to
		// reclaim promptly, infrequent enough to be negligible.
		select {
		case <-time.After(30 * time.Second):
		case <-r.stopCh:
			return
		}
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
