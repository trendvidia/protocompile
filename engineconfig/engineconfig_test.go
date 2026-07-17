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

package engineconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	schemav1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/v1"
)

// conformanceFixture is protowire's golden engine configuration,
// testdata/schema-extensions/08_engine_config.textproto — the loader
// conformance target named by RFC-001 §9.4.
const conformanceFixture = "testdata/08_engine_config.textproto"

// conformanceExpected is the effective configuration the conformance
// fixture must load to.
func conformanceExpected(path string, source Source) *Config {
	return &Config{
		FunctionLibraries: []string{"myco/commons/validator.proto", "myco/commons/types.proto"},
		CatalogLibraries:  []string{"myco/i18n/en.proto", "myco/i18n/de.proto"},
		Engine:            "cel",
		Path:              path,
		Source:            source,
		DefaultMode:       ExecutionModeCollectAll,
		MaxRecursionDepth: 64,
		StrictValidation:  true,
	}
}

// clearEnv makes sure PROTOWIRE_CONFIG from the invoking environment
// cannot leak into a test.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvVar, "")
}

// writeConfig writes content as dir's protowire.config.textproto and
// returns its path.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, FileName)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestConformanceFixture(t *testing.T) {
	clearEnv(t)

	t.Run("explicit path", func(t *testing.T) {
		cfg, err := Load(Options{ConfigPath: conformanceFixture})
		require.NoError(t, err)
		assert.Equal(t, conformanceExpected(conformanceFixture, SourceFlag), cfg)
	})

	t.Run("discovered", func(t *testing.T) {
		fixture, err := os.ReadFile(conformanceFixture)
		require.NoError(t, err)
		root := t.TempDir()
		path := writeConfig(t, root, string(fixture))
		nested := filepath.Join(root, "proto", "myco")
		require.NoError(t, os.MkdirAll(nested, 0o750))

		cfg, err := Load(Options{Dir: nested})
		require.NoError(t, err)
		assert.Equal(t, conformanceExpected(path, SourceDiscovered), cfg)
	})
}

func TestDiscoverNearestWins(t *testing.T) {
	clearEnv(t)

	root := t.TempDir()
	rootPath := writeConfig(t, root, `engine: "root"`)
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o750))
	nestedPath := writeConfig(t, filepath.Join(root, "a"), `engine: "nested" strict_validation: true`)
	uncovered := filepath.Join(root, "c")
	require.NoError(t, os.MkdirAll(uncovered, 0o750))

	tests := []struct {
		name       string
		dir        string
		wantEngine string
		wantPath   string
	}{
		{name: "nested config shadows root", dir: nested, wantEngine: "nested", wantPath: nestedPath},
		{name: "config in starting directory", dir: filepath.Join(root, "a"), wantEngine: "nested", wantPath: nestedPath},
		{name: "sibling tree falls through to root", dir: uncovered, wantEngine: "root", wantPath: rootPath},
		{name: "root uses its own config", dir: root, wantEngine: "root", wantPath: rootPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(Options{Dir: tt.dir})
			require.NoError(t, err)
			assert.Equal(t, tt.wantEngine, cfg.Engine)
			assert.Equal(t, tt.wantPath, cfg.Path)
			assert.Equal(t, SourceDiscovered, cfg.Source)

			// The nearest config wins outright: nothing from a config
			// higher up may leak in (no merging).
			assert.Equal(t, tt.wantEngine == "nested", cfg.StrictValidation)
		})
	}
}

func TestPrecedence(t *testing.T) {
	// Each tier gets its own file whose engine names the tier, so the
	// winning tier is visible in the loaded config.
	root := t.TempDir()
	discovered := writeConfig(t, root, `engine: "discovered"`)
	envFile := filepath.Join(root, "env.textproto")
	require.NoError(t, os.WriteFile(envFile, []byte(`engine: "env"`), 0o600))
	flagFile := filepath.Join(root, "flag.textproto")
	require.NoError(t, os.WriteFile(flagFile, []byte(`engine: "flag"`), 0o600))
	flagEngine := "flags"

	tests := []struct {
		name       string
		opts       Options
		env        string
		wantEngine string
		wantPath   string
		wantSource Source
	}{
		{
			name:       "per-setting flags beat explicit config path",
			opts:       Options{ConfigPath: flagFile, Flags: Overrides{Engine: &flagEngine}, Dir: root},
			env:        envFile,
			wantEngine: "flags",
			wantPath:   flagFile,
			wantSource: SourceFlag,
		},
		{
			name:       "explicit config path beats env var",
			opts:       Options{ConfigPath: flagFile, Dir: root},
			env:        envFile,
			wantEngine: "flag",
			wantPath:   flagFile,
			wantSource: SourceFlag,
		},
		{
			name:       "env var beats discovered file",
			opts:       Options{Dir: root},
			env:        envFile,
			wantEngine: "env",
			wantPath:   envFile,
			wantSource: SourceEnv,
		},
		{
			name:       "discovered file beats defaults",
			opts:       Options{Dir: root},
			wantEngine: "discovered",
			wantPath:   discovered,
			wantSource: SourceDiscovered,
		},
		{
			name:       "defaults when nothing is configured",
			opts:       Options{Dir: t.TempDir()},
			wantEngine: DefaultEngine,
			wantSource: SourceDefaults,
		},
		{
			name:       "flags apply on top of defaults",
			opts:       Options{Dir: t.TempDir(), Flags: Overrides{Engine: &flagEngine}},
			wantEngine: "flags",
			wantSource: SourceDefaults,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvVar, tt.env)
			cfg, err := Load(tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEngine, cfg.Engine)
			assert.Equal(t, tt.wantPath, cfg.Path)
			assert.Equal(t, tt.wantSource, cfg.Source)
		})
	}
}

func TestPerSettingOverrides(t *testing.T) {
	clearEnv(t)

	root := t.TempDir()
	writeConfig(t, root, `
		engine: "starlark"
		function_libraries: "a.proto"
		catalog_libraries: "en.proto"
		strict_validation: true
		default_mode: EXECUTION_MODE_FAIL_FAST
		max_recursion_depth: 16
	`)

	depth := uint32(8)
	mode := ExecutionModeCollectAll
	strict := false
	cfg, err := Load(Options{
		Dir: root,
		Flags: Overrides{
			FunctionLibraries: []string{"b.proto", "c.proto"},
			CatalogLibraries:  []string{},
			StrictValidation:  &strict,
			DefaultMode:       &mode,
			MaxRecursionDepth: &depth,
		},
	})
	require.NoError(t, err)

	// Un-overridden settings keep the file's values.
	assert.Equal(t, "starlark", cfg.Engine)
	// Overridden settings take the flag values, including a non-nil
	// empty slice overriding to "no libraries".
	assert.Equal(t, []string{"b.proto", "c.proto"}, cfg.FunctionLibraries)
	assert.Empty(t, cfg.CatalogLibraries)
	assert.False(t, cfg.StrictValidation)
	assert.Equal(t, ExecutionModeCollectAll, cfg.DefaultMode)
	assert.Equal(t, uint32(8), cfg.MaxRecursionDepth)
}

func TestFieldDefaultNormalization(t *testing.T) {
	clearEnv(t)

	t.Run("empty file resolves to built-in defaults", func(t *testing.T) {
		root := t.TempDir()
		path := writeConfig(t, root, "")
		cfg, err := Load(Options{Dir: root})
		require.NoError(t, err)
		want := &Config{
			Engine:            DefaultEngine,
			Path:              path,
			Source:            SourceDiscovered,
			DefaultMode:       ExecutionModeCollectAll,
			MaxRecursionDepth: DefaultMaxRecursionDepth,
		}
		assert.Equal(t, want, cfg)
	})

	t.Run("explicit zero values mean the normative defaults", func(t *testing.T) {
		root := t.TempDir()
		writeConfig(t, root, `default_mode: EXECUTION_MODE_UNSPECIFIED max_recursion_depth: 0`)
		cfg, err := Load(Options{Dir: root})
		require.NoError(t, err)
		assert.Equal(t, ExecutionModeCollectAll, cfg.DefaultMode)
		assert.Equal(t, uint32(DefaultMaxRecursionDepth), cfg.MaxRecursionDepth)
	})

	t.Run("zero-valued flags mean the normative defaults", func(t *testing.T) {
		depth := uint32(0)
		mode := ExecutionModeUnspecified
		cfg, err := Load(Options{
			Dir:   t.TempDir(),
			Flags: Overrides{MaxRecursionDepth: &depth, DefaultMode: &mode},
		})
		require.NoError(t, err)
		assert.Equal(t, ExecutionModeCollectAll, cfg.DefaultMode)
		assert.Equal(t, uint32(DefaultMaxRecursionDepth), cfg.MaxRecursionDepth)
	})
}

func TestHardErrors(t *testing.T) {
	engine := "starlark"
	tests := []struct {
		name    string
		setup   func(t *testing.T) Options
		env     func(t *testing.T, root string) string
		wantErr string
	}{
		{
			name: "explicit config path missing",
			setup: func(t *testing.T) Options {
				t.Helper()
				return Options{ConfigPath: filepath.Join(t.TempDir(), "nope.textproto")}
			},
			wantErr: "no such file",
		},
		{
			name: "env config path missing",
			setup: func(t *testing.T) Options {
				t.Helper()
				return Options{Dir: t.TempDir()}
			},
			env: func(t *testing.T, _ string) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "nope.textproto")
			},
			wantErr: "no such file",
		},
		{
			name: "malformed textproto",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `engine = "cel"`)
				return Options{Dir: root}
			},
			wantErr: "malformed engine config",
		},
		{
			name: "unknown field",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `motor: "cel"`)
				return Options{Dir: root}
			},
			wantErr: "malformed engine config",
		},
		{
			name: "unknown enum value name",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `default_mode: EXECUTION_MODE_WARP_SPEED`)
				return Options{Dir: root}
			},
			wantErr: "malformed engine config",
		},
		{
			name: "out-of-range enum number",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `default_mode: 99`)
				return Options{Dir: root}
			},
			wantErr: "invalid default_mode 99",
		},
		{
			name: "unknown engine from config file",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `engine: "quantum"`)
				return Options{Dir: root, KnownEngines: []string{"cel", "starlark"}}
			},
			wantErr: `unknown engine "quantum"`,
		},
		{
			name: "unknown engine from flag override",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				writeConfig(t, root, `engine: "cel"`)
				return Options{
					Dir:          root,
					Flags:        Overrides{Engine: &engine},
					KnownEngines: []string{"cel"},
				}
			},
			wantErr: `unknown engine "starlark"`,
		},
		{
			name: "default engine must be registered too",
			setup: func(t *testing.T) Options {
				t.Helper()
				return Options{Dir: t.TempDir(), KnownEngines: []string{"starlark"}}
			},
			wantErr: `unknown engine "cel"`,
		},
		{
			name: "config file name is a directory",
			setup: func(t *testing.T) Options {
				t.Helper()
				root := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(root, FileName), 0o750))
				return Options{Dir: root}
			},
			wantErr: "not a regular file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.setup(t)
			env := ""
			if tt.env != nil {
				env = tt.env(t, opts.Dir)
			}
			t.Setenv(EnvVar, env)
			cfg, err := Load(opts)
			require.Nil(t, cfg)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestExecutionModeMirrorsProto pins the package's ExecutionMode
// constants to the generated protowire.schema.v1.ExecutionMode values
// they mirror.
func TestExecutionModeMirrorsProto(t *testing.T) {
	assert.EqualValues(t, schemav1.ExecutionMode_EXECUTION_MODE_UNSPECIFIED, ExecutionModeUnspecified)
	assert.EqualValues(t, schemav1.ExecutionMode_EXECUTION_MODE_COLLECT_ALL, ExecutionModeCollectAll)
	assert.EqualValues(t, schemav1.ExecutionMode_EXECUTION_MODE_FAIL_FAST, ExecutionModeFailFast)

	assert.Equal(t, schemav1.ExecutionMode_EXECUTION_MODE_COLLECT_ALL.String(), ExecutionModeCollectAll.String())
	assert.Equal(t, schemav1.ExecutionMode_EXECUTION_MODE_FAIL_FAST.String(), ExecutionModeFailFast.String())
	assert.Equal(t, schemav1.ExecutionMode_EXECUTION_MODE_UNSPECIFIED.String(), ExecutionModeUnspecified.String())
}
