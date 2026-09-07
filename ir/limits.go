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

// MaxNumericLiteralDigits is protowire's hardening limit on the digit
// count of a numeric literal and on the magnitude of pxf.Decimal.scale on
// the wire (protowire HARDENING.md, "Mandatory limits"; draft -01
// § Mandatory Limits as amended by trendvidia/protowire#278). It is a
// decoder limit, and it binds the compiler through what the compiler
// emits: a binder renders a pxf.BigInt or pxf.Decimal default back into
// a PXF literal and refuses one past the cap, so a carrier beyond it is
// a schema no decoder accepts. It lives here, with the rest of the
// carrier semantics, rather than in the generic big-number helpers.
const MaxNumericLiteralDigits = 4096
