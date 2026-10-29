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

// Package pxf is an alias of [github.com/trendvidia/protos/pxf], where the
// generated code for pxf/bignum.proto now lives. It registers nothing
// itself, so a program may link both (trendvidia/protos#1).
//
// Deprecated: import github.com/trendvidia/protos/pxf.
package pxf

//go:generate go run ../../internal/cmd/genaliases -from github.com/trendvidia/protos/pxf -pkg pxf -o aliases.go
