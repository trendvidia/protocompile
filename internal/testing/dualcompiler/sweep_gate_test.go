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

package dualcompiler_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestAssertMustMatch_FiresOnRegression confirms that the
// equivalence gate actually fails when a mustMatch fixture
// downgrades, rather than silently passing. The whole point of the
// gate is to make regressions noisy; this test verifies it.
//
// It runs assertMustMatch against synthetic results inside a
// sub-test, captures whether the sub-test failed, and asserts the
// expected outcomes. The mustMatch list is shared package state, so
// the synthetic results pick a real entry from it to ensure the test
// keeps tracking the production gate.
func TestAssertMustMatch_FiresOnRegression(t *testing.T) {
	t.Parallel()

	if len(mustMatch) == 0 {
		t.Fatal("mustMatch is empty; the gate has nothing to enforce")
	}
	pinned := mustMatch[0]

	t.Run("all_match_passes", func(t *testing.T) {
		t.Parallel()

		results := make([]sweepResult, 0, len(mustMatch))
		for _, path := range mustMatch {
			results = append(results, sweepResult{Path: path, Category: "BOTH_OK_MATCH"})
		}

		mockT := newMockT()
		assertMustMatch(mockT, results)
		if mockT.failed {
			t.Errorf("gate failed unexpectedly when every mustMatch fixture was BOTH_OK_MATCH:\n%s",
				strings.Join(mockT.messages, "\n"))
		}
	})

	t.Run("regression_fails", func(t *testing.T) {
		t.Parallel()

		results := make([]sweepResult, 0, len(mustMatch))
		for _, path := range mustMatch {
			cat := "BOTH_OK_MATCH"
			if path == pinned {
				cat = "BOTH_OK_DIFFER"
			}
			results = append(results, sweepResult{Path: path, Category: cat})
		}

		mockT := newMockT()
		assertMustMatch(mockT, results)
		if !mockT.failed {
			t.Errorf("gate did not fire when fixture %q downgraded from "+
				"BOTH_OK_MATCH to BOTH_OK_DIFFER", pinned)
		}
		all := strings.Join(mockT.messages, "\n")
		if !strings.Contains(all, pinned) {
			t.Errorf("gate failure message did not mention the regressed fixture %q; got:\n%s",
				pinned, all)
		}
	})

	t.Run("missing_fixture_fails", func(t *testing.T) {
		t.Parallel()

		// Empty results: every mustMatch entry is "missing" from the
		// sweep. The gate should fail and mention that explicitly.
		mockT := newMockT()
		assertMustMatch(mockT, nil)
		if !mockT.failed {
			t.Error("gate did not fire when mustMatch fixtures were absent from the sweep")
		}
		all := strings.Join(mockT.messages, "\n")
		if !strings.Contains(all, "missing") {
			t.Errorf("gate failure message should call out missing fixtures; got:\n%s", all)
		}
	})
}

// mockT is a tiny stand-in for the testReporter interface that
// assertMustMatch accepts. It records Errorf calls instead of failing
// the parent test so we can observe whether the gate would have
// failed under real conditions.
type mockT struct {
	failed   bool
	messages []string
}

func newMockT() *mockT { return &mockT{} }

func (m *mockT) Errorf(format string, args ...any) {
	m.failed = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func (m *mockT) Helper() {}
