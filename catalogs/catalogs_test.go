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

package catalogs_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protocompile/catalogs"
	"github.com/trendvidia/protocompile/engineconfig"
)

func TestLoadSingleFile(t *testing.T) {
	t.Parallel()
	got, err := catalogs.Load("testdata", []string{"de.textproto"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	de := got["de"]
	require.NotNil(t, de)
	assert.Equal(t, "de", de.Locale)
	assert.Equal(t, map[string]string{
		"profile.username.format":     "Benutzername muss dem Muster {pattern} entsprechen",
		"profile.display_name.length": "Anzeigename ist zu lang",
	}, de.Entries)
	assert.Equal(t, []string{filepath.Join("testdata", "de.textproto")}, de.Paths)
}

func TestLoadMergesSameLocale(t *testing.T) {
	t.Parallel()
	got, err := catalogs.Load("testdata", []string{"de.textproto", "de_billing.textproto"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	de := got["de"]
	require.NotNil(t, de)
	assert.Equal(t, map[string]string{
		"profile.username.format":     "Benutzername muss dem Muster {pattern} entsprechen",
		"profile.display_name.length": "Anzeigename ist zu lang",
		"billing.amount.range":        "Betrag muss zwischen {min} und {max} liegen",
	}, de.Entries)
	assert.Equal(t, []string{
		filepath.Join("testdata", "de.textproto"),
		filepath.Join("testdata", "de_billing.textproto"),
	}, de.Paths)
}

func TestLoadMultipleLocales(t *testing.T) {
	t.Parallel()
	got, err := catalogs.Load("testdata", []string{"de.textproto", "en.textproto"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["de"])
	require.NotNil(t, got["en"])
	assert.Equal(t, "username must match {pattern}", got["en"].Entries["profile.username.format"])
}

func TestLoadEmptyPathsIsEmptyMap(t *testing.T) {
	t.Parallel()
	got, err := catalogs.Load("testdata", nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestLoadAbsolutePathIgnoresBaseDir(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs(filepath.Join("testdata", "en.textproto"))
	require.NoError(t, err)
	got, err := catalogs.Load(t.TempDir(), []string{abs})
	require.NoError(t, err)
	require.NotNil(t, got["en"])
	assert.Equal(t, []string{abs}, got["en"].Paths)
}

func TestLoadDuplicateCodeAcrossFiles(t *testing.T) {
	t.Parallel()
	_, err := catalogs.Load("testdata", []string{"de.textproto", "de_dup.textproto"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate code "profile.username.format" for locale "de"`)
	assert.Contains(t, err.Error(), filepath.Join("testdata", "de.textproto"))
	assert.Contains(t, err.Error(), filepath.Join("testdata", "de_dup.textproto"))
}

func TestLoadDuplicateCodeDifferentLocalesIsFine(t *testing.T) {
	t.Parallel()
	// de and en both declare profile.username.format — different locales,
	// no conflict.
	got, err := catalogs.Load("testdata", []string{"de.textproto", "en.textproto"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestLoadEmptyLocale(t *testing.T) {
	t.Parallel()
	_, err := catalogs.Load("testdata", []string{"empty_locale.textproto"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty locale")
}

func TestLoadMalformedFile(t *testing.T) {
	t.Parallel()
	_, err := catalogs.Load("testdata", []string{"malformed.textproto"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed.textproto")
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	_, err := catalogs.Load("testdata", []string{"no_such.textproto"})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "catalogs: "), err.Error())
}

func TestFromConfigResolvesAgainstConfigDir(t *testing.T) {
	// The §7 rule: catalog_libraries paths resolve relative to the
	// directory of the config file that declares them — here
	// testdata/project, regardless of the working directory.
	cfg, err := engineconfig.Load(engineconfig.Options{
		ConfigPath: filepath.Join("testdata", "project", "protowire.config.textproto"),
	})
	require.NoError(t, err)
	got, err := catalogs.FromConfig(cfg)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["de"])
	require.NotNil(t, got["en"])
	assert.Equal(t, "Benutzername muss dem Muster {pattern} entsprechen",
		got["de"].Entries["profile.username.format"])
}

func TestFromConfigNoConfigFileUsesWorkingDirectory(t *testing.T) {
	cfg := &engineconfig.Config{CatalogLibraries: []string{filepath.Join("testdata", "en.textproto")}}
	got, err := catalogs.FromConfig(cfg)
	require.NoError(t, err)
	require.NotNil(t, got["en"])
}
