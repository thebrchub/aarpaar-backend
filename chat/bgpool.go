package chat

import (
	"log"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Bounded Background Worker Pool
//
// Prevents unbounded goroutine growth during mass-disconnect events or
// spike traffic. All fire-and-forget background work (presence broadcasts,
// call disconnects, push notifications, call logging) goes through this
// pool instead of raw `go func()`.
//
// At 10K concurrent connections, a mass-disconnect could spawn ~30K+
// goroutines simultaneously. This pool caps concurrent background work
// to maxBackgroundWorkers, dropping excess tasks with a counter.
// ---------------------------------------------------------------------------

// maxBackgroundWorkers is the maximum number of concurrent background tasks.
// 200 is chosen to leave headroom for connection pools (Postgres ~25, Redis ~10)
// while still handling burst disconnect events efficiently.
const maxBackgroundWorkers = 200

var bgSem = make(chan struct{}, maxBackgroundWorkers)

// droppedBackgroundTasks counts tasks dropped because the pool was full.
var droppedBackgroundTasks atomic.Int64

// droppedMessages counts messages dropped because client send buffers were full.
// Incremented in deliverToRoom, deliverToUser, CloseRoom, sendError, and
// confirmation sends. Logged periodically by the metrics goroutine.
var droppedMessages atomic.Int64

// runBackground submits a function to the bounded worker pool.
// If the pool is full, the task is dropped and counted.
// All fire-and-forget goroutines should use this instead of bare `go func()`.
func runBackground(fn func()) {
	select {
	case bgSem <- struct{}{}:
		go func() {
			defer func() { <-bgSem }()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[bgpool] Recovered panic in background task: %v", r)
				}
			}()
			fn()
		}()
	default:
		droppedBackgroundTasks.Add(1)
	}
}
