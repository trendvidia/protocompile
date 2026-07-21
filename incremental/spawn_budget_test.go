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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/incremental"
)

// TestSpawnBudgetBoundsGoroutines verifies that the executor's
// spawn-budget cap keeps the number of in-flight async sub-task
// goroutines bounded regardless of how wide the sub-query fan-out gets.
// Without the cap, a depth-5 quadratic fan-out spawns hundreds of
// goroutines that all park on the global sema; with the cap, the
// executor never has more than `budget` spawned goroutines live at once.
//
// We assert against the executor's own high-water mark ([Executor.SpawnPeak])
// rather than runtime.NumGoroutine(). The latter is a process-global count
// that, under t.Parallel(), is polluted by goroutines spawned by other tests
// running concurrently, which makes the measurement flaky on loaded CI runners.
func TestSpawnBudgetBoundsGoroutines(t *testing.T) {
	t.Parallel()

	const budget = 4

	exec := incremental.New(
		incremental.WithParallelism(2),
		incremental.WithGoroutineBudget(budget),
	)

	// Depth-5 quadratic fan-out: under the old unbounded model this
	// spawned hundreds of goroutines (5! - 1), all parked on the global
	// semaphore.
	result, _, err := incremental.Run(t.Context(), exec, Fanout{Depth: 5})
	require.NoError(t, err)
	require.NoError(t, result[0].Fatal)
	assert.Equal(t, 1*2*3*4*5, result[0].Value)

	// Sanity check that the fan-out actually exercised the async spawn
	// path; otherwise the bound below would pass vacuously.
	assert.Positive(t, exec.SpawnPeak(),
		"fan-out did not spawn any async goroutines")

	assert.LessOrEqual(t, exec.SpawnPeak(), budget,
		"peak spawned goroutines above budget (peak=%d, budget=%d)",
		exec.SpawnPeak(), budget)
}
