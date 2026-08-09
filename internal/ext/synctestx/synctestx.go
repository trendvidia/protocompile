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

package synctestx

import (
	"runtime"
	"sync"
)

// Hammer runs f across count goroutines, ensuring that f is called
// simultaneously, simulating a thundering herd. Returns once all spawned
// goroutines have exited.
//
// If count is zero, uses GOMAXPROCS instead.
func Hammer(count int, f func()) {
	if count == 0 {
		count = runtime.GOMAXPROCS(0)
	}

	// Both counters are raised before any goroutine exists, so every Add
	// happens before every Wait. Adding inside the loop instead let a
	// goroutine drain the start counter to zero and enter Wait before the
	// next Add raised it again — a WaitGroup reuse, which panics under the
	// race detector, and which quietly released the barrier early when it
	// did not (#134).
	start := new(sync.WaitGroup)
	start.Add(count)
	end := new(sync.WaitGroup)
	end.Add(count)

	for range count {
		go func() {
			defer end.Done()

			// This ensures that we have a thundering herd situation: all of
			// these goroutines wake up and hammer f() at the same time.
			start.Done()
			start.Wait()

			f()
		}()
	}

	end.Wait()
}
