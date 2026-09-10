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
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/reporter"
)

// TestMissingImportNamesThePath is the repro from issue #223. A CLI that
// wraps the compiler shows Error() and nothing else, so the path that
// could not be found has to be in the message, not only in the snippet
// the full diagnostic renders under it.
func TestMissingImportNamesThePath(t *testing.T) {
	t.Parallel()

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if path == "config.proto" {
			return protocompile.SearchResult{Source: strings.NewReader(`syntax = "proto3";
package config;
import "widget/v1/widget.proto";
message Config { widget.v1.Widget widget = 1; }
`)}, nil
		}
		return protocompile.SearchResult{}, os.ErrNotExist
	})

	_, err := (&protocompile.Compiler{Resolver: res}).Compile(context.Background(), "config.proto")
	require.Error(t, err)
	var ewp reporter.ErrorWithPos
	require.ErrorAs(t, err, &ewp)
	assert.Equal(t, `config.proto:3:1: imported file "widget/v1/widget.proto" does not exist`, err.Error())
}
