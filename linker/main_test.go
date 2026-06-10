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

package linker

import (
	"os"
	"testing"
)

// TestMain pins this test package to the legacy pipeline for the M1
// deprecation window. The default-flip moves new code onto the
// experimental pipeline, but these tests assert legacy-specific
// behaviour that the experimental pipeline does not yet match
// (diagnostic wording, edge-case error timing, ...). Once Track C
// deletes the legacy pipeline these tests will either be rewritten
// against the experimental shape or removed entirely; this hook can
// then be dropped.
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv("PROTOCOMPILE_PARSER"); !set {
		os.Setenv("PROTOCOMPILE_PARSER", "legacy")
	}
	os.Exit(m.Run())
}
