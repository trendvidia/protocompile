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

package internal

// CanonicalHTTPFQN is the fully-qualified name of the canonical `@http`
// annotation, declared in protowire/schema/v1/annotations.proto.
//
// Two passes key on this name: the IR's RFC-001 §5.2 routing checks and
// the `fdp` lowering that emits the standard `google.api.http`
// extension. Keying on the resolved FQN leaves a user annotation that
// merely happens to be called `http` alone — but the two must agree, or
// routes get validated and never emitted (or emitted and never
// validated). Hence one spelling, in one place.
const CanonicalHTTPFQN = "protowire.schema.v1.http"

// GoogleAPIHTTPField is the field number of the standard
// `google.api.http` extension on google.protobuf.MethodOptions.
//
// `fdp` has the generated extension descriptor to ask, and does; `ir`
// does not link genproto and matches on the number alone. The two are
// pinned together by TestGoogleAPIHTTPFieldNumber.
const GoogleAPIHTTPField = 72295728
