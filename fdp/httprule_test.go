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

package fdp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/protocompile/fdp"
	pwsv1 "github.com/trendvidia/protocompile/gen/protowire/schema/v1"
	"github.com/trendvidia/protocompile/incremental"
	"github.com/trendvidia/protocompile/incremental/queries"
	"github.com/trendvidia/protocompile/ir"
	"github.com/trendvidia/protocompile/report"
	"github.com/trendvidia/protocompile/source"
)

// canonicalHTTPLib is the `@http` declaration of the canonical
// annotation library (protowire/schema/v1/annotations.proto), verbatim
// in its own package: the §5.2 lowering keys on the resolved FQN, so
// the test schema has to declare it where the real library does.
const canonicalHTTPLib = `syntax = "proto3";
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

// compileHTTPTest compiles main.proto against the canonical annotation
// library and returns its descriptor plus the compile report.
func compileHTTPTest(t *testing.T, main string, options ...fdp.DescriptorOption) (*descriptorpb.FileDescriptorProto, *report.Report) {
	t.Helper()
	return compileHTTPTestFiles(t, map[string]string{
		"main.proto":        main,
		"annotations.proto": canonicalHTTPLib,
	}, options...)
}

// compileHTTPTestFiles compiles a file set whose entry point is
// main.proto.
func compileHTTPTestFiles(t *testing.T, files map[string]string, options ...fdp.DescriptorOption) (*descriptorpb.FileDescriptorProto, *report.Report) {
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
	require.NotNil(t, results[0].Value)

	out, err := fdp.DescriptorProto(results[0].Value, options...)
	require.NoError(t, err)
	return out, rep
}

// requireNoErrors fails the test on any error-level diagnostic.
func requireNoErrors(t *testing.T, rep *report.Report) {
	t.Helper()
	if rep == nil {
		return
	}
	for _, d := range rep.Diagnostics {
		if d.Level() >= report.Error {
			t.Fatalf("unexpected diagnostic: %s", d.Message())
		}
	}
}

// httpRuleOf returns the google.api.http rule on a method, or nil.
func httpRuleOf(mdp *descriptorpb.MethodDescriptorProto) *annotations.HttpRule {
	if mdp.GetOptions() == nil || !proto.HasExtension(mdp.GetOptions(), annotations.E_Http) {
		return nil
	}
	rule, _ := proto.GetExtension(mdp.GetOptions(), annotations.E_Http).(*annotations.HttpRule)
	return rule
}

// TestHTTPRuleEmission verifies RFC-001 §5.2 / protowire#213: the
// routing skeleton of `@http` lowers to a standard google.api.http
// rule beside the annotation carrier — the verb selecting the pattern,
// bodyless verbs taking no body and everything else `body: "*"`, and
// the positional and named argument spellings reading alike.
func TestHTTPRuleEmission(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "annotations.proto";

message ListOrdersRequest { int32 page_size = 1; }
message ListOrdersResponse { string next = 1; }
message GetOrderRequest { string order_id = 1; }
message CreateOrderRequest { string sku = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders")
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);

  @http(method = "GET", path = "/orders/{order_id}",
        summary = "Fetch an order", tags = ["orders"])
  rpc GetOrder(GetOrderRequest) returns (Order);

  @http("POST", "/orders")
  rpc CreateOrder(CreateOrderRequest) returns (Order);

  @http("delete", "/orders/{order_id}")
  rpc DeleteOrder(GetOrderRequest) returns (Order);

  @http("WATCH", "/orders/{order_id}")
  rpc WatchOrder(GetOrderRequest) returns (Order);
}
`

	f, rep := compileHTTPTest(t, main)
	requireNoErrors(t, rep)
	require.Len(t, f.GetService(), 1)
	methods := f.GetService()[0].GetMethod()
	require.Len(t, methods, 5)

	list := methods[0].GetOptions()
	require.NotNil(t, list)

	rule := httpRuleOf(methods[0])
	require.NotNil(t, rule, "ListOrders should carry google.api.http")
	assert.Equal(t, "/orders", rule.GetGet())
	assert.Empty(t, rule.GetBody(), "bodyless verbs bind unbound fields to the query string")

	rule = httpRuleOf(methods[1])
	require.NotNil(t, rule)
	assert.Equal(t, "/orders/{order_id}", rule.GetGet(),
		"named-argument spelling reads the same as positional")

	rule = httpRuleOf(methods[2])
	require.NotNil(t, rule)
	assert.Equal(t, "/orders", rule.GetPost())
	assert.Equal(t, "*", rule.GetBody(),
		"body verbs bind every field the path template did not")

	rule = httpRuleOf(methods[3])
	require.NotNil(t, rule)
	assert.Equal(t, "/orders/{order_id}", rule.GetDelete(), "the verb is case-insensitive")
	assert.Empty(t, rule.GetBody())

	rule = httpRuleOf(methods[4])
	require.NotNil(t, rule)
	require.NotNil(t, rule.GetCustom(), "a verb outside HttpRule's five is a custom pattern")
	assert.Equal(t, "WATCH", rule.GetCustom().GetKind())
	assert.Equal(t, "/orders/{order_id}", rule.GetCustom().GetPath())
	assert.Equal(t, "*", rule.GetBody())
}

// TestHTTPRuleEmissionCarrierAlongside verifies the standard extension
// is emitted *beside* the annotation carrier, never instead of it: the
// carrier keeps the enriched operation surface HttpRule cannot express.
func TestHTTPRuleEmissionCarrierAlongside(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "annotations.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{order_id}",
        summary = "Fetch an order",
        operation_id = "getOrder",
        tags = ["orders", "public"],
        security = ["bearerAuth"])
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	f, rep := compileHTTPTest(t, main)
	requireNoErrors(t, rep)
	mdp := f.GetService()[0].GetMethod()[0]

	carrier, ok := proto.GetExtension(mdp.GetOptions(), pwsv1.E_MethodAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, carrier.GetEntries(), 1)
	assert.Equal(t, "protowire.schema.v1.http", carrier.GetEntries()[0].GetName())
	assert.Len(t, carrier.GetEntries()[0].GetArgs(), 6,
		"the carrier keeps every argument, including the operation metadata")

	rule := httpRuleOf(mdp)
	require.NotNil(t, rule)
	assert.Equal(t, "/orders/{order_id}", rule.GetGet())
}

// TestHTTPRuleEmissionAdditionalBindings verifies a method carrying
// several `@http` use sites lowers the first as the primary rule and
// the rest as additional_bindings, in source order.
func TestHTTPRuleEmissionAdditionalBindings(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "annotations.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{order_id}")
  @http("GET", "/v2/orders/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	f, rep := compileHTTPTest(t, main)
	requireNoErrors(t, rep)

	rule := httpRuleOf(f.GetService()[0].GetMethod()[0])
	require.NotNil(t, rule)
	assert.Equal(t, "/orders/{order_id}", rule.GetGet())
	require.Len(t, rule.GetAdditionalBindings(), 1)
	assert.Equal(t, "/v2/orders/{order_id}", rule.GetAdditionalBindings()[0].GetGet())
}

// TestHTTPRuleEmissionSuppressed verifies EmitGoogleAPIHTTP(false)
// drops the standard extension and leaves the carrier untouched.
func TestHTTPRuleEmissionSuppressed(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "annotations.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	f, rep := compileHTTPTest(t, main, fdp.EmitGoogleAPIHTTP(false))
	requireNoErrors(t, rep)
	mdp := f.GetService()[0].GetMethod()[0]

	assert.Nil(t, httpRuleOf(mdp), "opted out, so no google.api.http")
	carrier, ok := proto.GetExtension(mdp.GetOptions(), pwsv1.E_MethodAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	assert.Len(t, carrier.GetEntries(), 1, "the carrier is unaffected by the opt-out")
}

// TestHTTPRuleEmissionNonCanonical verifies the lowering keys on the
// resolved FQN: a user annotation that happens to be named `http` is
// carried like any other and produces no routing rule.
func TestHTTPRuleEmissionNonCanonical(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

annotation http(method: string, path: string);

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/orders/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`

	f, rep := compileHTTPTestFiles(t, map[string]string{"main.proto": main})
	requireNoErrors(t, rep)
	mdp := f.GetService()[0].GetMethod()[0]

	assert.Nil(t, httpRuleOf(mdp))
	carrier, ok := proto.GetExtension(mdp.GetOptions(), pwsv1.E_MethodAnnotations).(*pwsv1.AnnotationList)
	require.True(t, ok)
	require.Len(t, carrier.GetEntries(), 1)
	assert.Equal(t, "fixtures.http.http", carrier.GetEntries()[0].GetName())
}

// TestHTTPRuleEmissionAuthoredWins verifies an author-written
// (google.api.http) is left alone rather than joined by a second,
// competing rule at the same field number.
func TestHTTPRuleEmissionAuthoredWins(t *testing.T) {
	t.Parallel()

	// A minimal stand-in for google/api/annotations.proto: only the
	// extension number and the two pattern fields the fixture sets
	// matter for the wire bytes this test inspects.
	const googleAPI = `syntax = "proto3";
package google.api;

import "google/protobuf/descriptor.proto";

message HttpRule {
  string get = 2;
  string post = 4;
}

extend google.protobuf.MethodOptions {
  HttpRule http = 72295728;
}
`
	const main = `syntax = "proto3";
package fixtures.http;

import "annotations.proto";
import "google/api/annotations.proto";

message GetOrderRequest { string order_id = 1; }
message Order { string order_id = 1; }

service Orders {
  @http("GET", "/carrier/{order_id}")
  rpc GetOrder(GetOrderRequest) returns (Order) {
    option (google.api.http) = { get: "/authored/{order_id}" };
  }
}
`

	f, rep := compileHTTPTestFiles(t, map[string]string{
		"main.proto":                   main,
		"annotations.proto":            canonicalHTTPLib,
		"google/api/annotations.proto": googleAPI,
	})
	requireNoErrors(t, rep)
	mdp := f.GetService()[0].GetMethod()[0]

	// User-written options live in unknown-field bytes until a
	// round-trip folds them into typed fields.
	raw, err := proto.Marshal(mdp.GetOptions())
	require.NoError(t, err)
	opts := new(descriptorpb.MethodOptions)
	require.NoError(t, proto.Unmarshal(raw, opts))

	rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	require.True(t, ok)
	require.NotNil(t, rule)
	assert.Equal(t, "/authored/{order_id}", rule.GetGet(),
		"the authored rule stands; the lowering does not add a second one")
	assert.Empty(t, rule.GetAdditionalBindings())

	// Two occurrences would merge on decode rather than fail, so the
	// count is what proves the guard fired: the field must appear once.
	assert.Equal(t, 1, countField(raw, annotations.E_Http.TypeDescriptor().Number()),
		"exactly one (google.api.http) on the wire")
}

// countField reports how many times a field number appears at the top
// level of an encoded message.
func countField(b []byte, want protowire.Number) int {
	var n int
	for len(b) > 0 {
		num, kind, tagLen := protowire.ConsumeTag(b)
		if tagLen < 0 {
			return n
		}
		b = b[tagLen:]
		valLen := protowire.ConsumeFieldValue(num, kind, b)
		if valLen < 0 {
			return n
		}
		b = b[valLen:]
		if num == want {
			n++
		}
	}
	return n
}
