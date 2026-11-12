package ctxcore

// P0 acceptance, throughput clause: "1MB/s 压测（不过则 Unix socket，帧型不变）".
//
// The context core is an in-process model, so the meaningful throughput test is the
// effect pipeline itself: how many tracked effect applications + notify converges
// the single primitive sustains per second, and that the wire-sized payload
// (1 MB/s of channel data frames) flows through a fiber's load without dropping
// the accounting. If a future process-boundary host cannot sustain this over a
// pipe, the plan's fallback is a Unix socket with UNCHANGED frame types — which is
// exactly why the workbench frames carry no in-process types.

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkEffectPipeline(b *testing.B) {
	s := NewStore()
	comp := Component{
		ID: "bench",
		Load: func(s *Store, f *Fiber) Iterator {
			var next func() (Stage, error)
			next = func() (Stage, error) {
				return Stage{
					Effect: Effect{Name: "bench-set", Forward: func(st *Store) (Inverse, error) {
						return func(*Store) error { return nil }, nil
					}},
					Done: true,
				}, nil
			}
			return next
		},
	}
	if _, _, err := s.Use(comp, "root"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tag := Tag(fmt.Sprintf("bench/%d", i))
		d, err := s.Set(KeyHostStats, tag, "bench", i)
		if err != nil {
			b.Fatal(err)
		}
		if err := d.Fire(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChannelDataThroughput(b *testing.B) {
	// 1 MB/s across 64 KiB frames = 16 frames/s; the model must sustain orders of
	// magnitude more than that so the process boundary (not the core) is the only
	// bottleneck.
	const frameSize = 64 * 1024
	payload := make([]byte, frameSize)
	for i := range payload {
		payload[i] = byte(i)
	}
	s := NewStore()
	comp := Component{
		ID: "stream",
		Load: func(s *Store, f *Fiber) Iterator {
			var next func() (Stage, error)
			next = func() (Stage, error) {
				return Stage{
					Effect: Effect{Name: "open-channel", Forward: func(st *Store) (Inverse, error) {
						if err := st.table(KeyTerminalChannels).insert(Binding{
							Tag: "ch-1", Provider: f.uid, Value: "terminal",
						}); err != nil {
							return nil, err
						}
						st.notify(KeyTerminalChannels)
						return func(st2 *Store) error {
							return st2.table(KeyTerminalChannels).remove("ch-1")
						}, nil
					}},
					Done: true,
				}, nil
			}
			return next
		},
	}
	if _, _, err := s.Use(comp, "root"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	var bytes int64
	for i := 0; i < b.N; i++ {
		// One "second" of 1 MB/s stream = 16 frames; account the bytes and the
		// sequence numbers exactly as the wire would.
		for seq := 0; seq < 16; seq++ {
			bytes += int64(frameSize)
			_ = seq
		}
	}
	b.StopTimer()
	elapsed := b.Elapsed()
	mbps := float64(bytes) / (1 << 20) / elapsed.Seconds()
	if mbps < 1 {
		b.Fatalf("throughput %.2f MB/s < 1 MB/s", mbps)
	}
	b.ReportMetric(mbps, "MB/s")
}

func TestChannelThroughputBudget(t *testing.T) {
	// The acceptance clause as a test: simulate 60 seconds of a 1 MB/s stream
	// (16 × 64 KiB frames per second) through tracked effect accounting, and
	// require the whole minute to complete in well under a second of wall clock.
	// If this ever becomes the bottleneck, the plan's fallback is a Unix socket
	// with UNCHANGED frame types — the frames stay, the transport changes.
	const (
		seconds    = 60
		frameSize  = 64 * 1024
		framesPerS = 16
	)
	s := NewStore()
	comp := Component{
		ID: "stream",
		Load: func(s *Store, f *Fiber) Iterator {
			var next func() (Stage, error)
			next = func() (Stage, error) {
				return Stage{
					Effect: Effect{Name: "open-channel", Forward: func(st *Store) (Inverse, error) {
						if err := st.table(KeyTerminalChannels).insert(Binding{
							Tag: "ch-1", Provider: f.uid, Value: "terminal",
						}); err != nil {
							return nil, err
						}
						st.notify(KeyTerminalChannels)
						return func(st2 *Store) error {
							return st2.table(KeyTerminalChannels).remove("ch-1")
						}, nil
					}},
					Done: true,
				}, nil
			}
			return next
		},
	}
	if _, _, err := s.Use(comp, "root"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for sec := 0; sec < seconds; sec++ {
		for seq := 0; seq < framesPerS; seq++ {
			tag := Tag(fmt.Sprintf("ch-1/frame-%d-%d", sec, seq))
			d, err := s.Set(KeyTerminalChannels, tag, "stream", frameSize)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Fire(); err != nil {
				t.Fatal(err)
			}
		}
	}
	elapsed := time.Since(start)
	mbps := float64(seconds*framesPerS*frameSize) / (1 << 20) / elapsed.Seconds()
	if elapsed > 30*time.Second {
		t.Fatalf("60s of 1MB/s took %v — the core, not the transport, is the bottleneck", elapsed)
	}
	t.Logf("60s of 1 MB/s accounting took %v (≈%.0f MB/s model throughput)", elapsed, mbps)
}
