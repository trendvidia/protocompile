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

// Package engineconfig implements the reference loader for protowire
// project-level engine configuration (RFC-001 §9.4).
//
// A project's engine selection and engine-level knobs live in a single
// protowire.config.textproto file — a text-format
// protowire.schema.config.v1.EngineConfig message — at the project root.
// This package is the shared implementation of the discovery and
// precedence rules, so that protocheck, protolsp, and CLI tooling all
// resolve the same effective configuration:
//
//   - Discovery walks upward from the working directory (or an explicit
//     schema root) to the filesystem root; the nearest config file wins.
//     There is no merging or cascading between nested configs.
//   - Precedence, highest first: per-setting CLI flags ([Overrides]),
//     an explicit config path (--config), the PROTOWIRE_CONFIG
//     environment variable (a pointer to a file, never inline settings),
//     the discovered file, and finally built-in defaults.
//
// Malformed configuration is a hard error: an unreadable or unparseable
// file, an invalid execution mode, or an engine name missing from
// [Options].KnownEngines fails loading outright — there is no fallback
// to defaults and no partial load.
package engineconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	configv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/config/v1"
)

const (
	// FileName is the name of the engine configuration file that
	// discovery looks for: a text-format
	// protowire.schema.config.v1.EngineConfig message.
	FileName = "protowire.config.textproto"

	// EnvVar is the environment variable that points at an engine
	// configuration file. It is a file pointer only — it never carries
	// inline settings — and is consulted only when no explicit config
	// path is given.
	EnvVar = "PROTOWIRE_CONFIG"

	// DefaultEngine is the built-in default engine identifier, used when
	// no configuration source sets one.
	DefaultEngine = "cel"

	// DefaultMaxRecursionDepth is the normative default for
	// max_recursion_depth (RFC-001 §6.4). A configured value of 0 means
	// this default.
	DefaultMaxRecursionDepth = 64
)

// ExecutionMode is the default execution mode for validation, mirroring
// protowire.schema.v1.ExecutionMode value-for-value.
type ExecutionMode int32

const (
	// ExecutionModeUnspecified is the zero value; it means
	// [ExecutionModeCollectAll] (RFC-001 §6.4). [Load] never returns it:
	// the effective configuration is normalized to a concrete mode.
	ExecutionModeUnspecified ExecutionMode = 0
	// ExecutionModeCollectAll collects every violation before returning.
	ExecutionModeCollectAll ExecutionMode = 1
	// ExecutionModeFailFast stops at the first violation.
	ExecutionModeFailFast ExecutionMode = 2
)

// String returns the protowire.schema.v1.ExecutionMode name for m.
func (m ExecutionMode) String() string {
	switch m {
	case ExecutionModeUnspecified:
		return "EXECUTION_MODE_UNSPECIFIED"
	case ExecutionModeCollectAll:
		return "EXECUTION_MODE_COLLECT_ALL"
	case ExecutionModeFailFast:
		return "EXECUTION_MODE_FAIL_FAST"
	default:
		return fmt.Sprintf("ExecutionMode(%d)", int32(m))
	}
}

// Source identifies which precedence tier supplied the configuration
// file that [Config] was loaded from. Per-setting flag overrides apply
// on top of any tier and do not change the source.
type Source int

const (
	// SourceDefaults means no configuration file was found or given; the
	// configuration is the built-in defaults.
	SourceDefaults Source = iota
	// SourceDiscovered means the file was found by walking upward from
	// the starting directory.
	SourceDiscovered
	// SourceEnv means the file was named by the PROTOWIRE_CONFIG
	// environment variable.
	SourceEnv
	// SourceFlag means the file was named by an explicit config path
	// ([Options].ConfigPath, i.e. --config).
	SourceFlag
)

// String returns a human-readable name for s.
func (s Source) String() string {
	switch s {
	case SourceDefaults:
		return "defaults"
	case SourceDiscovered:
		return "discovered"
	case SourceEnv:
		return "env"
	case SourceFlag:
		return "flag"
	default:
		return fmt.Sprintf("Source(%d)", int(s))
	}
}

// Config is an effective engine configuration: the result of resolving
// every precedence tier and normalizing field-level defaults. It mirrors
// protowire.schema.config.v1.EngineConfig, except that Engine is never
// empty, DefaultMode is never [ExecutionModeUnspecified], and
// MaxRecursionDepth is never 0.
type Config struct {
	// FunctionLibraries lists import paths (resolved like proto imports)
	// of schema files whose function declarations the engine must
	// implement (RFC-001 §9.2, §9.3).
	FunctionLibraries []string
	// CatalogLibraries lists locale catalog sources registered with the
	// engine for i18n message rendering (RFC-001 §7).
	CatalogLibraries []string
	// Engine is the registered engine identifier, e.g. "cel",
	// "starlark", "go".
	Engine string
	// Path is the configuration file the non-default settings came from.
	// It is empty when Source is [SourceDefaults].
	Path string
	// Source is the precedence tier that supplied Path.
	Source Source
	// DefaultMode is the execution mode used when a caller of Validate()
	// does not choose one.
	DefaultMode ExecutionMode
	// MaxRecursionDepth is the maximum message-nesting depth validated
	// (RFC-001 §6.4).
	MaxRecursionDepth uint32
	// StrictValidation makes missing function implementations fail
	// engine startup instead of failing at first call (RFC-001 §9.2).
	StrictValidation bool
}

// Overrides carries per-setting CLI flag values — the highest precedence
// tier. A nil field means the flag was not set and the underlying
// configuration value is kept.
type Overrides struct {
	// FunctionLibraries replaces the configured function_libraries when
	// non-nil. A non-nil empty slice overrides to no libraries.
	FunctionLibraries []string
	// CatalogLibraries replaces the configured catalog_libraries when
	// non-nil. A non-nil empty slice overrides to no libraries.
	CatalogLibraries []string
	// Engine replaces the configured engine when non-nil.
	Engine *string
	// StrictValidation replaces the configured strict_validation when
	// non-nil.
	StrictValidation *bool
	// DefaultMode replaces the configured default_mode when non-nil.
	DefaultMode *ExecutionMode
	// MaxRecursionDepth replaces the configured max_recursion_depth when
	// non-nil. 0 means the normative default,
	// [DefaultMaxRecursionDepth].
	MaxRecursionDepth *uint32
}

// Options configures [Load].
type Options struct {
	// Flags carries per-setting CLI flag overrides, applied on top of
	// whichever configuration file (or defaults) wins.
	Flags Overrides
	// KnownEngines lists the engine identifiers registered with the
	// calling tool. When non-nil, resolving to an engine outside the
	// list is a hard error (RFC-001 §9.4: an unknown engine name is a
	// startup error, never a silent fallback). When nil, the caller
	// resolves the engine name against its registry itself.
	KnownEngines []string
	// Dir is the directory discovery starts from: the working directory
	// or an explicit schema root. It defaults to the current working
	// directory. It is ignored when ConfigPath or the PROTOWIRE_CONFIG
	// environment variable selects a file explicitly.
	Dir string
	// ConfigPath is an explicit configuration file path (--config). When
	// set it is loaded directly, skipping the environment variable and
	// discovery; a missing or malformed file is a hard error.
	ConfigPath string
}

// Load resolves the effective engine configuration per RFC-001 §9.4.
//
// The configuration file is selected by precedence: opts.ConfigPath if
// set, else the file named by the PROTOWIRE_CONFIG environment variable
// if set, else the nearest protowire.config.textproto found by walking
// upward from opts.Dir. If none is found, built-in defaults apply.
// opts.Flags then overrides individual settings, and field-level
// defaults are normalized (empty engine → "cel", unspecified mode →
// collect-all, zero depth → 64).
func Load(opts Options) (*Config, error) {
	cfg := &Config{Source: SourceDefaults}

	path, source := "", SourceDefaults
	switch envPath := os.Getenv(EnvVar); {
	case opts.ConfigPath != "":
		path, source = opts.ConfigPath, SourceFlag
	case envPath != "":
		path, source = envPath, SourceEnv
	default:
		discovered, err := Discover(opts.Dir)
		if err != nil {
			return nil, err
		}
		if discovered != "" {
			path, source = discovered, SourceDiscovered
		}
	}
	if path != "" {
		msg, err := parseFile(path)
		if err != nil {
			return nil, err
		}
		cfg.Engine = msg.GetEngine()
		cfg.FunctionLibraries = msg.GetFunctionLibraries()
		cfg.CatalogLibraries = msg.GetCatalogLibraries()
		cfg.StrictValidation = msg.GetStrictValidation()
		cfg.DefaultMode = ExecutionMode(msg.GetDefaultMode())
		cfg.MaxRecursionDepth = msg.GetMaxRecursionDepth()
		cfg.Path = path
		cfg.Source = source
	}

	flags := opts.Flags
	if flags.Engine != nil {
		cfg.Engine = *flags.Engine
	}
	if flags.FunctionLibraries != nil {
		cfg.FunctionLibraries = flags.FunctionLibraries
	}
	if flags.CatalogLibraries != nil {
		cfg.CatalogLibraries = flags.CatalogLibraries
	}
	if flags.StrictValidation != nil {
		cfg.StrictValidation = *flags.StrictValidation
	}
	if flags.DefaultMode != nil {
		cfg.DefaultMode = *flags.DefaultMode
	}
	if flags.MaxRecursionDepth != nil {
		cfg.MaxRecursionDepth = *flags.MaxRecursionDepth
	}

	switch cfg.DefaultMode {
	case ExecutionModeUnspecified:
		cfg.DefaultMode = ExecutionModeCollectAll
	case ExecutionModeCollectAll, ExecutionModeFailFast:
	default:
		return nil, fmt.Errorf("engineconfig: %s: invalid default_mode %d", sourceForError(cfg), int32(cfg.DefaultMode))
	}
	if cfg.Engine == "" {
		cfg.Engine = DefaultEngine
	}
	if cfg.MaxRecursionDepth == 0 {
		cfg.MaxRecursionDepth = DefaultMaxRecursionDepth
	}

	if opts.KnownEngines != nil && !slices.Contains(opts.KnownEngines, cfg.Engine) {
		return nil, fmt.Errorf("engineconfig: %s: unknown engine %q (registered engines: %s)",
			sourceForError(cfg), cfg.Engine, strings.Join(opts.KnownEngines, ", "))
	}

	return cfg, nil
}

// Discover walks upward from dir (defaulting to the current working
// directory) to the filesystem root and returns the path of the nearest
// protowire.config.textproto, or "" if there is none. Nested
// configurations do not merge or cascade — the nearest file wins, full
// stop.
func Discover(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("engineconfig: resolving discovery root: %w", err)
	}
	for {
		path := filepath.Join(dir, FileName)
		info, err := os.Stat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			return path, nil
		case err == nil:
			return "", fmt.Errorf("engineconfig: %s: not a regular file", path)
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("engineconfig: %w", err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// parseFile reads and parses one engine configuration file. Any failure
// — unreadable file, malformed text format, unknown fields — is a hard
// error; there is no fallback and no partial load.
func parseFile(path string) (*configv1.EngineConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the config file selected by the caller.
	if err != nil {
		return nil, fmt.Errorf("engineconfig: %w", err)
	}
	msg := new(configv1.EngineConfig)
	if err := prototext.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("engineconfig: %s: malformed engine config: %w", path, err)
	}
	return msg, nil
}

// sourceForError names the configuration source in error messages: the
// file path when one was loaded, "built-in defaults" otherwise. Flag
// overrides can introduce errors on top of either.
func sourceForError(cfg *Config) string {
	if cfg.Path != "" {
		return cfg.Path
	}
	return "built-in defaults"
}
