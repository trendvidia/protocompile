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

	"github.com/trendvidia/protocompile/report"
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
// `{name}` segment of an `@http` path binds to the same-named
// top-level field of the request message. The skeleton lowers to a
// google.api.http rule, so a segment that binds nothing is rejected at
// compile time rather than 404ing in every REST binder downstream
// (protowire#213).
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
// variable names a real field, and a path with no template at all.
func TestHTTPTemplateAccepted(t *testing.T) {
	t.Parallel()

	const main = `syntax = "proto3";
package fixtures.http;

import "lib.proto";

message GetOrderRequest {
  string order_id = 1;
  string revision = 2;
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
}
`

	_, rep := compileTwoForAnnotationTest(t, main, httpLib)
	for _, d := range rep.Diagnostics {
		if d.Level() >= report.Error {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
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
		if d.Level() >= report.Error {
			t.Errorf("unexpected diagnostic: %s", d.Message())
		}
	}
}
