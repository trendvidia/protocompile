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

package reporter

import "fmt"

// SourcePos identifies a location in a proto source file.
type SourcePos struct {
	Filename string
	// The line and column numbers for this position. These are
	// one-based, so the first line and column is 1 (not zero). If
	// either is zero, then the line and column are unknown and
	// only the file name is known.
	Line, Col int
	// The offset, in bytes, from the beginning of the file. This
	// is zero-based: the first character in the file is offset zero.
	Offset int
}

func (pos SourcePos) String() string {
	if pos.Line <= 0 || pos.Col <= 0 {
		return pos.Filename
	}
	return fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Col)
}

// SourceSpan represents a range of source positions.
type SourceSpan interface {
	Start() SourcePos
	End() SourcePos
}

// NewSourceSpan creates a new span that covers the given range.
func NewSourceSpan(start SourcePos, end SourcePos) SourceSpan {
	return sourceSpan{StartPos: start, EndPos: end}
}

type sourceSpan struct {
	StartPos SourcePos
	EndPos   SourcePos
}

func (p sourceSpan) Start() SourcePos {
	return p.StartPos
}

func (p sourceSpan) End() SourcePos {
	return p.EndPos
}

// UnknownPos is a placeholder position when only the source file
// name is known.
func UnknownPos(filename string) SourcePos {
	return SourcePos{Filename: filename}
}

// UnknownSpan is a placeholder span when only the source file name
// is known.
func UnknownSpan(filename string) SourceSpan {
	return unknownSpan{filename: filename}
}

type unknownSpan struct {
	filename string
}

func (s unknownSpan) Start() SourcePos {
	return UnknownPos(s.filename)
}

func (s unknownSpan) End() SourcePos {
	return UnknownPos(s.filename)
}
