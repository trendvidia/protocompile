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

package ir_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/trendvidia/protocompile/ir"
)

// TestSymbolKindTables checks every [ir.SymbolKind] against the tables
// indexed by it. A kind missing from the end of a table panics when a
// diagnostic or a dump describes a symbol of that kind (issue #297), so the
// kinds are counted from the generated table to include any added later.
func TestSymbolKindTables(t *testing.T) {
	t.Parallel()

	for k := range ir.SymbolKind(ir.SymbolKindCount) {
		assert.NotPanics(t, func() { k.OptionTarget() }, "%v", k)
		if k != ir.SymbolKindInvalid {
			assert.NotEmpty(t, k.Noun(), "%v", k)
		}
	}
}
