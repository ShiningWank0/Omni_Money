package database

import (
	"context"
	"sync"
)

// LedgerWork keeps already accepted work alive independently of HTTP sessions.
// The instance owner waits for it before destroying the database or its key.
type ledgerWork struct {
	mu                        sync.Mutex
	cond                      *sync.Cond
	once                      sync.Once
	work                      func()
	running, pending, closing bool
}

func (i *Instance) initLedgerWork() {
	i.ledgerWork.once.Do(func() { i.ledgerWork.cond = sync.NewCond(&i.ledgerWork.mu) })
}

// RegisterLedgerWorker installs one runner for this exact database instance.
// On the first registration it resumes durable work left by an earlier process.
func (i *Instance) RegisterLedgerWorker(work func()) {
	i.initLedgerWork()
	i.ledgerWork.mu.Lock()
	first := i.ledgerWork.work == nil && !i.ledgerWork.closing
	if first {
		i.ledgerWork.work = work
	}
	i.ledgerWork.mu.Unlock()
	if first {
		i.WakeLedgerWorker()
	}
}

func (i *Instance) WakeLedgerWorker() {
	i.initLedgerWork()
	i.ledgerWork.mu.Lock()
	defer i.ledgerWork.mu.Unlock()
	if i.ledgerWork.closing || i.ledgerWork.work == nil {
		return
	}
	i.ledgerWork.pending = true
	if i.ledgerWork.running {
		return
	}
	i.ledgerWork.running = true
	go func() {
		for {
			i.ledgerWork.mu.Lock()
			if !i.ledgerWork.pending {
				i.ledgerWork.running = false
				i.ledgerWork.cond.Broadcast()
				i.ledgerWork.mu.Unlock()
				return
			}
			i.ledgerWork.pending = false
			work := i.ledgerWork.work
			i.ledgerWork.mu.Unlock()
			work()
		}
	}()
}

func (i *Instance) WaitForLedgerWork(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	i.initLedgerWork()
	stop := context.AfterFunc(ctx, func() {
		i.ledgerWork.mu.Lock()
		i.ledgerWork.cond.Broadcast()
		i.ledgerWork.mu.Unlock()
	})
	defer stop()
	i.ledgerWork.mu.Lock()
	defer i.ledgerWork.mu.Unlock()
	for i.ledgerWork.running && ctx.Err() == nil {
		i.ledgerWork.cond.Wait()
	}
	return ctx.Err()
}

func (i *Instance) stopLedgerWork() {
	i.initLedgerWork()
	i.ledgerWork.mu.Lock()
	i.ledgerWork.closing = true
	for i.ledgerWork.running {
		i.ledgerWork.cond.Wait()
	}
	i.ledgerWork.mu.Unlock()
}

// Init closes an earlier handle before publishing a newly opened database.
// Reset only after that close has joined every previous worker.
func (i *Instance) resetLedgerWork() {
	i.initLedgerWork()
	i.ledgerWork.mu.Lock()
	i.ledgerWork.closing = false
	i.ledgerWork.pending = false
	i.ledgerWork.work = nil
	i.ledgerWork.mu.Unlock()
}
