// Copyright 2020-2026 Buf Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package incremental_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/incremental"
)

// TestSpawnBudgetBoundsGoroutines verifies that the executor's
// spawn-budget cap keeps the in-flight goroutine count bounded
// regardless of how wide the sub-query fan-out gets. Without the cap,
// a depth-5 quadratic fan-out spawns hundreds of goroutines that all
// park on the global sema; with the cap, the live goroutine count
// stays within a small multiple of the budget.
func TestSpawnBudgetBoundsGoroutines(t *testing.T) {
	t.Parallel()

	const budget = 4

	exec := incremental.New(
		incremental.WithParallelism(2),
		incremental.WithGoroutineBudget(budget),
	)

	// Sample the goroutine count every few ms while the query is
	// in flight. The compute work inside Fanout is trivial (just
	// arithmetic), so observed goroutine count is dominated by
	// in-flight task goroutines.
	baseline := runtime.NumGoroutine()
	var peak atomic.Int32
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				n := int32(runtime.NumGoroutine() - baseline)
				for {
					prev := peak.Load()
					if n <= prev || peak.CompareAndSwap(prev, n) {
						break
					}
				}
			}
		}
	}()

	// Depth-5 quadratic fan-out: under the old unbounded model this
	// spawns hundreds of goroutines (5! - 1).
	_, _, err := incremental.Run(t.Context(), exec, Fanout{Depth: 5})
	close(stop)
	require.NoError(t, err)

	// Allow a generous slack: workload goroutines (the sampler, test
	// runner internals, etc.) plus up to 2x the budget for async
	// tasks in flight at any moment.
	const slack = 16
	limit := int32(budget + slack)
	assert.LessOrEqual(t, peak.Load(), limit,
		"peak goroutines above budget (peak=%d, limit=%d)", peak.Load(), limit)
}
