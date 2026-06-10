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

package protocompile_test

import (
	"os"
	"testing"
)

// TestMain pins the protocompile_test package to the legacy pipeline
// for the M1 deprecation window. The default-flip moves new code onto
// the experimental pipeline, but the existing tests in this package
// (TestPanicHandling, TestWarningReporting, TestParseFilesWithDependencies,
// ...) assert legacy-specific diagnostic wording and error timing
// that the experimental pipeline does not yet match.
//
// Setting the env var in TestMain — rather than the field on every
// constructed [protocompile.Compiler] — means each test stays
// readable. Tests that explicitly want experimental can override
// either by setting [protocompile.Compiler.UseExperimentalParser]
// directly or by unsetting the env var inside the test.
//
// Once Track C deletes the legacy pipeline these tests will either be
// rewritten against the experimental shape or removed entirely; this
// hook can then be dropped.
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv("PROTOCOMPILE_PARSER"); !set {
		os.Setenv("PROTOCOMPILE_PARSER", "legacy")
	}
	os.Exit(m.Run())
}
