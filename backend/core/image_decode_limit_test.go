package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestImageDecodeSlotsBoundParallelWork(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, err := acquireImageDecodeSlot(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			current := active.Add(1)
			for {
				old := peak.Load()
				if current <= old || peak.CompareAndSwap(old, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			release()
		}()
	}
	close(start)
	wg.Wait()
	if got := peak.Load(); got > maxConcurrentImageDecodes {
		t.Fatalf("peak decode workers=%d, limit=%d", got, maxConcurrentImageDecodes)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("test did not exercise concurrency: peak=%d", got)
	}
}

func TestImageDecodeSlotWaitHonorsCancellation(t *testing.T) {
	releases := make([]func(), 0, maxConcurrentImageDecodes)
	for i := 0; i < maxConcurrentImageDecodes; i++ {
		release, err := acquireImageDecodeSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if _, err := acquireImageDecodeSlot(ctx); err == nil {
		t.Fatal("cancelled decode wait succeeded")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled wait took %s", elapsed)
	}
}
