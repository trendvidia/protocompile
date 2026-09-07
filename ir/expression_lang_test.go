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

package ir

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseExpressionCalls pins what the §5.4 parser extracts: every
// call site with its written name, byte ranges, arity and form.
func TestParseExpressionCalls(t *testing.T) {
	t.Parallel()

	src := `!this.starts_with("@") && t.in_region(this, ["US", "CA"]) && size(this) > 0 || now() < this`
	calls, err := parseExpression(src)
	require.Nil(t, err)
	require.Len(t, calls, 4)

	assert.Equal(t, exprCall{name: "starts_with", nameStart: 6, nameEnd: 17, start: 6, end: 22, args: 1, method: true}, calls[0])
	assert.Equal(t, "t.in_region", calls[1].name)
	assert.Equal(t, 2, calls[1].args, "the comma inside the list does not count")
	assert.False(t, calls[1].method)
	assert.Equal(t, `t.in_region(this, ["US", "CA"])`, src[calls[1].start:calls[1].end])
	assert.Equal(t, exprCall{name: "size", nameStart: 61, nameEnd: 65, start: 61, end: 71, args: 1}, calls[2])
	assert.Equal(t, exprCall{name: "now", nameStart: 79, nameEnd: 82, start: 79, end: 84}, calls[3])
}

// TestParseExpressionAccepts pins the shapes the grammar admits,
// including the lexical forms the reference engine lexes.
func TestParseExpressionAccepts(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		`this`,
		`true`,
		`this >= -1 && this < 2.5`,
		`this in ["US", "CA", 'GB']`,
		`this in []`,
		`(this == "a") || !(this != "b")`,
		`this.size() >= 2 && size(this) <= 16`,
		`matches(this, "^[^@]+@[^@]+$") && this.matches("[.]")`,
		`this > now()`,
		"this > 0 // trailing comment\n && this < 10",
		"/* leading */ this.contains(\"a\\\"b\")",
		`same_domain(this)`,
		`a.b.c(this, 1, "x", [1, 2], now())`,
	} {
		_, err := parseExpression(src)
		assert.Nil(t, err, "%q: %v", src, err)
	}
}

// TestParseExpressionRejects pins the first syntax error's message and
// byte range for the shapes the grammar refuses.
func TestParseExpressionRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		src        string
		want       string
		start, end int
	}{
		{`this >= && this < 150`, "expected an expression, got `&&`", 8, 10},
		{`this.name == "x"`, "field selection is not part of the expression language (`this.name`)", 5, 9},
		{`foo > 1`, "`foo` is not bound", 0, 3},
		{`this < 1 < 2`, "comparison operators do not chain", 9, 10},
		{`this + 1`, "unexpected character '+': the expression language has no arithmetic", 5, 6},
		{`"unterminated`, "unterminated string literal", 0, 13},
		{`this.size(`, "expected an expression, got the end of the expression", 10, 10},
		{`this.size(1`, "expected `,` or `)` in the argument list opened at offset 9", 11, 11},
		{`this in [1, 2`, "expected `,` or `]` in the list opened at offset 8", 13, 13},
		{`(this`, "expected `)` to close the parenthesis opened at offset 0", 5, 5},
		{`this.`, "expected a method name after `.`", 5, 5},
		{`this in in`, "expected an expression, got `in`", 8, 10},
		{`this.size() this`, "unexpected `this` after the expression", 12, 16},
	}
	for _, tc := range cases {
		calls, err := parseExpression(tc.src)
		require.NotNil(t, err, "%q parsed: %v", tc.src, calls)
		assert.Contains(t, err.msg, tc.want, "%q", tc.src)
		assert.Equal(t, []int{tc.start, tc.end}, []int{err.start, err.end}, "%q: %s", tc.src, err.msg)
	}
}
