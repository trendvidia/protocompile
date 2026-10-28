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

package protocompile

import (
	// Blank-import the experimental compile pipeline so its `init` runs
	// at program start and the experimental implementation registers
	// with [exphook]. [Compiler.Compile] always routes through it: the
	// legacy pipeline, and the `PROTOCOMPILE_PARSER=legacy` env var that
	// selected it, were removed in Track C.
	//
	// This blank import is only possible because the experimental
	// pipeline does not depend on this package transitively: the
	// experimental sub-packages import the dependency-free
	// `wellknownimports/fs` sub-package for their embedded data, and
	// `experimentalcompile` itself imports `internal/exphook` rather
	// than this package for its contract.
	_ "github.com/trendvidia/protocompile/experimentalcompile"
)
