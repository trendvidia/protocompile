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

package descpath_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/fdp/descpath"
)

// TestRoundTrip verifies the RFC-001 §8.3.1 grammar's
// worked examples format and parse losslessly.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path   string
		parsed descpath.Path
	}{
		{
			"myco.User.email",
			descpath.Path{Element: "myco.User.email"},
		},
		{
			"myco.User.email[protowire.schema.v1.validate#1]",
			descpath.Path{
				Element:    "myco.User.email",
				Annotation: "protowire.schema.v1.validate",
				Ordinal:    1,
			},
		},
		{
			"myco.User[myco.check#0]/arg#0/call#1",
			descpath.Path{
				Element:    "myco.User",
				Annotation: "myco.check",
				HasCall:    true,
				CallIndex:  1,
			},
		},
		{
			// File-level annotation in a packageless file: empty
			// element path, so the path begins with '['.
			"[myco.check#0]",
			descpath.Path{Annotation: "myco.check"},
		},
		{
			// Enum values use their parent-scoped name.
			"pkg.OK[pkg.description#0]",
			descpath.Path{Element: "pkg.OK", Annotation: "pkg.description"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			parsed, err := descpath.Parse(tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.parsed, parsed)
			assert.Equal(t, tc.path, parsed.String())
		})
	}
}

// TestParseErrors verifies the parser is strict about
// shapes outside the grammar.
func TestParseErrors(t *testing.T) {
	t.Parallel()

	bad := []string{
		".myco.User",                    // leading dot
		"myco..User",                    // empty component
		"myco.User[validate]",           // anchor without ordinal
		"myco.User[validate#01]",        // leading zero
		"myco.User[validate#-1]",        // sign
		"myco.User[validate#0",          // unterminated anchor
		"myco.User[validate#0]/arg#0",   // call anchor missing /call#
		"myco.User[validate#0]/call#0",  // call anchor missing /arg#
		"myco.User[validate#0]trailing", // junk after anchor
		"myco.User/arg#0/call#0",        // call anchor without annotation anchor
		"myco.9User[validate#0]",        // identifier starting with a digit
	}
	for _, s := range bad {
		_, err := descpath.Parse(s)
		assert.Error(t, err, "expected parse failure for %q", s)
	}
}

// TestImportsNoCompiler pins the reason this package exists: importing it must
// not link any other package of this module.
func TestImportsNoCompiler(t *testing.T) {
	t.Parallel()

	const self = "github.com/trendvidia/protocompile/fdp/descpath"
	out, err := exec.Command("go", "list", "-deps", self).Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	require.Contains(t, deps, self)
	for _, dep := range deps {
		if dep != self && strings.HasPrefix(dep, "github.com/trendvidia/protocompile") {
			t.Errorf("descpath imports %s", dep)
		}
	}
}
