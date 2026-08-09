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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// httpLib is the canonical `@http` declaration in the package the
// §5.2 checks key on.
const httpLib = `syntax = "proto3";
package protowire.schema.v1;

annotation http(
  method: string,
  path: string,
  summary: string = "",
  operation_id: string = "",
  tags: any = [],
  security: any = []
);
`

// TestHTTPTemplateBindsRequestField verifies RFC-001 §5.2: every
// `{name}` segment of an `@http` path names a field of the request
// message. The skeleton lowers to a google.api.http rule, so a segment
// that binds nothing is rejected at compile time rather than 404ing in
// every REST binder downstream (protowire#213).
func TestHTTPTemplateBindsRequestField(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest {
  string order_id = 1;
}
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{orderId}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t,
		hasErrorContaining(rep, "path template", "{orderId}", "fixtures.http.GetOrderRequest"),
		"expected unbound-template diagnostic, got: %v", rep.Diagnostics)
}

// TestHTTPTemplateAccepted verifies the shapes that do bind compile
// clean: the positional and named spellings, a sub-path pattern whose
// variable names a real field, a dotted path into a nested message,
// and a path with no template at all.
func TestHTTPTemplateAccepted(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message OrderRef {
  string id = 1;
}
message GetOrderRequest {
  string order_id = 1;
  string revision = 2;
  OrderRef ref = 3;
}
message ListOrdersRequest { int32 page_size = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders")
  rpc ListOrders(ListOrdersRequest) returns (Order);

  @http(method = "GET", path = "/orders/{order_id}/{revision}")
  rpc GetOrder(GetOrderRequest) returns (Order);

  @http("GET", "/shelves/{order_id=shelves/**}")
  rpc GetShelvedOrder(GetOrderRequest) returns (Order);

  @http("GET", "/refs/{ref.id}")
  rpc GetOrderByRef(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	for _, d := range rep.Diagnostics {
		if d.Level() <= report.Error {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// TestHTTPTemplateNestedFieldMustExist verifies a dotted path is
// resolved rather than waved through: the leading component binds, the
// one after it names nothing, and the route is still unservable.
func TestHTTPTemplateNestedFieldMustExist(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message OrderRef { string id = 1; }
message GetOrderRequest {
  OrderRef ref = 1;
  string order_id = 2;
}
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/refs/{ref.nope}")
  rpc GetOrderByRef(GetOrderRequest) returns (Order);

  @http("GET", "/refs/{order_id.nope}")
  rpc GetOrderByScalarPath(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t, hasErrorContaining(rep, "{ref.nope}", "binds no field"),
		"expected unbound nested-path diagnostic, got: %v", rep.Diagnostics)
	assert.True(t, hasErrorContaining(rep, "{order_id.nope}", "binds no field"),
		"expected diagnostic for descending through a scalar, got: %v", rep.Diagnostics)
}

// TestHTTPTemplateRejectsRepeatedField verifies a path variable bound
// to a repeated or map field is rejected: a variable expands to one
// value, so `google.api.HttpRule` refuses the binding and the route is
// as unservable as one that binds nothing at all.
func TestHTTPTemplateRejectsRepeatedField(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message TagRef { string id = 1; }
message ListOrdersRequest {
  repeated string tags = 1;
  map<string, string> labels = 2;
  repeated TagRef refs = 3;
}
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{tags}")
  rpc ByTag(ListOrdersRequest) returns (Order);

  @http("GET", "/orders/{labels}")
  rpc ByLabel(ListOrdersRequest) returns (Order);

  @http("GET", "/orders/{refs.id}")
  rpc ByRef(ListOrdersRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t, hasErrorContaining(rep, "{tags}", "repeated field"),
		"expected repeated-binding diagnostic, got: %v", rep.Diagnostics)
	assert.True(t, hasErrorContaining(rep, "{labels}", "repeated field"),
		"expected map-binding diagnostic, got: %v", rep.Diagnostics)
	assert.True(t, hasErrorContaining(rep, "{refs.id}", "repeated field"),
		"expected diagnostic for a repeated component mid-path, got: %v", rep.Diagnostics)
}

// TestHTTPPathMustBeAbsolute verifies a relative path is rejected: an
// HttpRule whose path does not start with `/` binds no route.
func TestHTTPPathMustBeAbsolute(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "orders/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t, hasErrorContaining(rep, "path must be absolute"),
		"expected absolute-path diagnostic, got: %v", rep.Diagnostics)
}

// TestHTTPPathMalformedTemplate verifies an unclosed `{` is diagnosed
// as a malformed template rather than read as a literal path segment.
func TestHTTPPathMalformedTemplate(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{order_id")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t, hasErrorContaining(rep, "malformed", "path template"),
		"expected malformed-template diagnostic, got: %v", rep.Diagnostics)
}

// TestHTTPMethodMustNotBeEmpty verifies an empty verb is rejected: it
// selects no HttpRule pattern.
func TestHTTPMethodMustNotBeEmpty(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("", "/orders/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	assert.True(t, hasErrorContaining(rep, "method must not be empty"),
		"expected empty-method diagnostic, got: %v", rep.Diagnostics)
}

// TestHTTPAuthoredRuleWarns verifies a method carrying both `@http`
// and an author-written `(google.api.http)` is warned about: the
// authored rule wins in the descriptor lowering, so the annotation's
// route reaches the carrier but binds nothing, and that divergence is
// otherwise silent at every stage downstream.
func TestHTTPAuthoredRuleWarns(t *testing.T) {
	t.Parallel()

	// A minimal stand-in for google/api/annotations.proto: the checks
	// key on the extension's field number, which is all this needs to
	// carry faithfully.
	const googleAPI = `syntax = "proto3";
package google.api;

import "google/protobuf/descriptor.proto";

message HttpRule { string get = 2; }

extend google.protobuf.MethodOptions {
  HttpRule http = 72295728;
}
`
	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";
import "google/api/annotations.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/carrier/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order) {
    option (google.api.http) = { get: "/authored/{order_id}" };
  }

  @http("GET", "/uncontested/{order_id}")
  rpc ListOrders(GetOrderRequest) returns (Order);
}
`

	_, rep := compileHTTPFiles(t, map[string]string{
		"main.proto":                   main,
		"lib.proto":                    httpLib,
		"google/api/annotations.proto": googleAPI,
	})

	var warnings int
	for _, d := range rep.Diagnostics {
		switch {
		case d.Level() <= report.Error:
			t.Errorf("unexpected diagnostic: %s", d.Message())
		case strings.Contains(d.Message(), "does not route this method"):
			warnings++
		}
	}
	assert.Equal(t, 1, warnings,
		"one warning, on the contested method only: %v", rep.Diagnostics)
}

// compileHTTPFiles compiles a file set whose entry point is main.proto.
// Unlike compileTwoForAnnotationTest it takes an arbitrary set, for the
// fixtures that need a stand-in google/api/annotations.proto beside the
// annotation library.
func compileHTTPFiles(t *testing.T, files map[string]string) (*ir.File, *report.Report) {
	t.Helper()

	sources := make(map[string]*source.File, len(files))
	for name, text := range files {
		sources[name] = source.NewFile(name, text)
	}
	allOpeners := &source.Openers{source.NewMap(sources), source.WKTs()}

	results, rep, err := incremental.Run(t.Context(), incremental.New(), queries.IR{
		Opener:  allOpeners,
		Session: new(ir.Session),
		Path:    "main.proto",
	})
	require.NoError(t, err)
	require.NotNil(t, rep)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Value)
	return results[0].Value, rep
}

// TestHTTPChecksAreCanonicalOnly verifies the §5.2 checks key on the
// resolved FQN: a user annotation that happens to be named `http`
// carries whatever it likes and is never route-checked.
func TestHTTPChecksAreCanonicalOnly(t *testing.T) {
	t.Parallel()

	const lib = `syntax = "proto3";
package other.pkg;

annotation http(method: string, path: string);
`
	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "nowhere/{nothing}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	_, rep := compileTwoForAnnotationTest(t, main, lib)
	for _, d := range rep.Diagnostics {
		if d.Level() <= report.Error {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}
