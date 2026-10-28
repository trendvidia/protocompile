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

func TestCyclic(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	ctx := t.Context()
	exec := incremental.New(
		incremental.WithParallelism(4),
	)

	result, _, err := incremental.Run(ctx, exec, Cyclic{Mod: 5, Step: 3})
	require.NoError(t, err)
	assert.Equal(
		`cycle detected: `+
			`incremental_test.Cyclic{Mod:5, Step:3} -> `+
			`incremental_test.Cyclic{Mod:5, Step:4} -> `+
			`incremental_test.Cyclic{Mod:5, Step:0} -> `+
			`incremental_test.Cyclic{Mod:5, Step:1} -> `+
			`incremental_test.Cyclic{Mod:5, Step:2} -> `+
			`incremental_test.Cyclic{Mod:5, Step:3}`,
		result[0].Fatal.Error(),
	)
}

// Several queries entering one cycle at once each detect it while another
// task leads the query they wait on. Each must get its own cycle error:
// the detecting waiter wrote it into the leader's result, which every
// waiter shares, so two waiters raced on it (go test -race), and a
// waiter's cycle could stand in for the leader's own result.
func TestCyclicConcurrentEntries(t *testing.T) {
	t.Parallel()
	for range 50 {
		exec := incremental.New(incremental.WithParallelism(8))
		queries := []incremental.Query[int]{
			Cyclic{Mod: 5, Step: 0}, Cyclic{Mod: 5, Step: 1}, Cyclic{Mod: 5, Step: 2},
			Cyclic{Mod: 5, Step: 3}, Cyclic{Mod: 5, Step: 4},
		}
		results, _, err := incremental.Run(t.Context(), exec, queries...)
		require.NoError(t, err)
		for i, r := range results {
			require.Error(t, r.Fatal, "query %d", i)
			assert.Contains(t, r.Fatal.Error(), "cycle detected", "query %d", i)
		}
	}
}

// Cyclic is a query that queries itself, for triggering cycle detection.
type Cyclic struct {
	Mod, Step int
}

func (c Cyclic) Key() any {
	return c
}

func (c Cyclic) Execute(t *incremental.Task) (int, error) {
	next, err := incremental.Resolve(t, Cyclic{
		Mod:  c.Mod,
		Step: (c.Step + 1) % c.Mod,
	})
	if err != nil {
		return 0, err
	}

	// NOTE: This call is a regression check against a case where calling
	// Report() after a cyclic error would incorrectly treat the cycle point
	// as having been completed.
	t.Report().Remarkf("squaring: %d", next[0].Value)
	return next[0].Value * next[0].Value, next[0].Fatal
}
