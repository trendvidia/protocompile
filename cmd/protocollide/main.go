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

// Command protocollide reports Protobuf namespace collisions between
// modules, so that two modules vendoring the same `.proto` are caught in
// CI rather than as an `init()` panic in whatever binary first links both.
//
// Usage:
//
//	protocollide <name>=<dir> <name>=<dir> [...]
//
// Each argument names a module and the import root to check it under, so a
// file at <dir>/pxf/annotations.proto is imported as
// "pxf/annotations.proto". Exits 0 when no name is claimed twice, 1 when
// any is, and 2 when the arguments were invalid or a module could not be
// read or compiled.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/trendvidia/protocompile/collide"
)

const (
	exitOK       = 0
	exitCollided = 1
	exitError    = 2
)

func main() {
	// os.Exit skips deferred calls, so the signal handler is torn down
	// explicitly before exiting rather than with a defer that never runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("protocollide", flag.ContinueOnError)
	flags.SetOutput(stderr)
	quiet := flags.Bool("q", false, "print nothing; report only through the exit code")
	flags.Usage = func() {
		fmt.Fprint(stderr, usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		// -h is a request, not a failure; ContinueOnError reports it as one.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}

	mods, err := parseModules(flags.Args())
	if err != nil {
		fmt.Fprintf(stderr, "protocollide: %v\n", err)
		flags.Usage()
		return exitError
	}

	collisions, err := collide.Check(ctx, mods)
	if err != nil {
		fmt.Fprintf(stderr, "protocollide: %v\n", err)
		return exitError
	}

	if len(collisions) == 0 {
		if !*quiet {
			fmt.Fprintf(stdout, "protocollide: %s, no collisions\n", plural(len(mods), "module"))
		}
		return exitOK
	}

	if !*quiet {
		for _, c := range collisions {
			fmt.Fprintf(stdout, "%s\n", c)
		}
		fmt.Fprintf(stdout, "\nprotocollide: %s\n", summary(collisions))
	}
	return exitCollided
}

func summary(collisions []collide.Collision) string {
	var files, symbols int
	for _, c := range collisions {
		if c.Kind == collide.KindFile {
			files++
			continue
		}
		symbols++
	}
	return fmt.Sprintf(
		"%s (%s, %s); linking these modules together would panic in protoregistry at init",
		plural(len(collisions), "collision"), plural(files, "file path"), plural(symbols, "symbol"))
}

// plural renders a count with its noun, pluralised the boring way.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// parseModules turns `name=dir` arguments into modules.
func parseModules(args []string) ([]collide.Module, error) {
	if len(args) < 2 {
		return nil, errors.New("need at least two modules to compare")
	}
	mods := make([]collide.Module, 0, len(args))
	for _, arg := range args {
		// Go's flag package stops at the first operand, so a flag written
		// after the modules arrives here looking like a module.
		if strings.HasPrefix(arg, "-") {
			return nil, fmt.Errorf("flag %q must come before the module arguments", arg)
		}
		name, dir, ok := strings.Cut(arg, "=")
		if !ok || name == "" || dir == "" {
			return nil, fmt.Errorf("argument %q is not of the form <name>=<dir>", arg)
		}
		mods = append(mods, collide.Module{Name: name, Root: dir})
	}
	return mods, nil
}

const usage = `protocollide reports Protobuf namespace collisions between modules.

Two modules that each vendor a copy of the same .proto register the same
file path and the same fully-qualified names into the process-global
registry from init(), so any binary linking both panics before main runs.
Per-module linting cannot see this: each module is individually clean, and
the collision exists only in the link graph.

Usage:
  protocollide [flags] <name>=<dir> <name>=<dir> [...]

Each <dir> is an import root: a file at <dir>/pxf/annotations.proto is
imported as "pxf/annotations.proto". Names are labels for the report and
are not interpreted.

Example:
  protocollide chameleon=../chameleon/proto voya=./proto

Exit codes:
  0  no name claimed twice
  1  at least one collision, listed on stdout
  2  bad arguments, or a module that could not be read or compiled

Flags:
`
