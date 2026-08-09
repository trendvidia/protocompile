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

package synctestx_test

import (
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/trendvidia/protocompile/internal/ext/synctestx"
)

// TestHammerRepeated is the regression test for #134.
//
// [synctestx.Hammer] used to add to the start barrier inside the spawn loop,
// so a goroutine could call Done (draining the counter to zero) and enter Wait
// before the parent's next Add — reusing the WaitGroup, which panics with
// "sync: WaitGroup is reused before previous Wait has returned" and races on
// the counter word. The window is one scheduling decision wide, so a single
// Hammer call almost always gets away with it; the failure surfaced once in a
// whole-suite -race run and not at all under `-count=200` of the one test that
// called it. Hammering Hammer itself is what makes it deterministic.
func TestHammerRepeated(t *testing.T) {
	t.Parallel()

	for i := range 2000 {
		var calls atomic.Int64
		synctestx.Hammer(4, func() { calls.Add(1) })
		if got := calls.Load(); got != 4 {
			t.Fatalf("iteration %d: f ran %d times, want 4", i, got)
		}
	}
}

// TestHammerDefaultCount covers the count == 0 spelling every caller in the
// tree uses, where the herd size is GOMAXPROCS.
func TestHammerDefaultCount(t *testing.T) {
	t.Parallel()

	want := int64(runtime.GOMAXPROCS(0))
	for range 500 {
		var calls atomic.Int64
		synctestx.Hammer(0, func() { calls.Add(1) })
		if got := calls.Load(); got != want {
			t.Fatalf("f ran %d times, want GOMAXPROCS = %d", got, want)
		}
	}
}

// TestHammerSingle covers the degenerate herd: one goroutine, which is the
// shape most likely to expose an off-by-one in the barrier arithmetic.
func TestHammerSingle(t *testing.T) {
	t.Parallel()

	for range 2000 {
		var calls atomic.Int64
		synctestx.Hammer(1, func() { calls.Add(1) })
		if got := calls.Load(); got != 1 {
			t.Fatalf("f ran %d times, want 1", got)
		}
	}
}

// TestHammerWaitsForF pins the documented postcondition — Hammer returns only
// once every goroutine has finished f, not merely once they have started it.
// A barrier fix that released the end WaitGroup early would still pass the
// call-count tests above.
func TestHammerWaitsForF(t *testing.T) {
	t.Parallel()

	for range 500 {
		var running, peak atomic.Int64
		synctestx.Hammer(4, func() {
			n := running.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			running.Add(-1)
		})
		if got := running.Load(); got != 0 {
			t.Fatalf("Hammer returned with %d goroutine(s) still in f", got)
		}
	}
}
