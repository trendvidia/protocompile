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

package fdp

import "github.com/trendvidia/protocompile/fdp/descpath"

// DescriptorPath is the parsed form of a source-map entry's descriptor_path.
// See [descpath.Path], which holds the grammar and the formatter.
type DescriptorPath = descpath.Path

// ParseDescriptorPath parses a canonical descriptor_path string. See
// [descpath.Parse].
func ParseDescriptorPath(s string) (DescriptorPath, error) {
	return descpath.Parse(s)
}
