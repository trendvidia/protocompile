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

// Package catalogs implements the reference loader for protowire locale
// catalog sources (RFC-001 §7).
//
// A catalog library file — a path named by an engine configuration's
// catalog_libraries (§9.4, the [engineconfig] package) — is a text-format
// protowire.schema.catalog.v1.Catalog message: one locale per file,
// entries keyed by violation code, values are message templates whose
// {name} placeholders interpolate a violation's params at format time.
// This package loads and merges those files into plain per-locale data;
// rendering belongs to the engine (protocheck's MapCatalog consumes
// [Catalog].Entries directly).
//
// Per §7, paths resolve relative to the directory of the config file
// that declares them — they are not proto import paths, and build tools
// do not compile catalogs into descriptor images. Multiple files may
// declare the same locale (per-domain catalogs); they merge into one
// catalog per locale, and the same code appearing in two files for one
// locale is a load error, never a silent override. Malformed input — an
// unreadable file, invalid text format, an unknown field, an empty
// locale — is a hard error: there is no fallback and no partial load.
package catalogs

import (
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/trendvidia/protocompile/engineconfig"
	catalogv1 "github.com/trendvidia/protocompile/internal/gen/protowire/schema/catalog/v1"
)

// Catalog is one locale's merged message catalog: every entry from every
// catalog library file that declared the locale.
type Catalog struct {
	// Locale is the BCP 47 language tag the catalog is registered under
	// (Engine.RegisterCatalog, RFC-001 §9.1). Locale negotiation —
	// matching a consumer's requested locale against loaded catalogs —
	// is the consumer's policy, not this package's.
	Locale string
	// Entries maps violation code → message template. `{name}`
	// placeholders interpolate the violation's params; a placeholder
	// with no matching param is left verbatim (§7).
	Entries map[string]string
	// Paths lists the files the catalog was merged from, in load order.
	Paths []string
}

// Load reads the catalog library files named by paths and merges them
// into one [Catalog] per locale, keyed by locale. Relative paths resolve
// against baseDir; "" means the current directory. Absolute paths are
// taken as-is. An empty paths list returns an empty, non-nil map.
func Load(baseDir string, paths []string) (map[string]*Catalog, error) {
	byLocale := make(map[string]*Catalog, len(paths))
	// origin remembers, per locale, which file contributed each code, so
	// a duplicate is reported naming both ends.
	origin := make(map[string]map[string]string, len(paths))
	for _, path := range paths {
		resolved := path
		if !filepath.IsAbs(path) {
			resolved = filepath.Join(baseDir, path)
		}
		msg, err := parseFile(resolved)
		if err != nil {
			return nil, err
		}
		locale := msg.GetLocale()
		if locale == "" {
			return nil, fmt.Errorf("catalogs: %s: empty locale", resolved)
		}
		cat := byLocale[locale]
		if cat == nil {
			cat = &Catalog{Locale: locale, Entries: make(map[string]string, len(msg.GetEntries()))}
			byLocale[locale] = cat
			origin[locale] = make(map[string]string, len(msg.GetEntries()))
		}
		for code, tmpl := range msg.GetEntries() {
			if prev, ok := origin[locale][code]; ok {
				return nil, fmt.Errorf("catalogs: %s: duplicate code %q for locale %q (also in %s)",
					resolved, code, locale, prev)
			}
			origin[locale][code] = resolved
			cat.Entries[code] = tmpl
		}
		cat.Paths = append(cat.Paths, resolved)
	}
	return byLocale, nil
}

// FromConfig loads the catalogs of a resolved engine configuration:
// [Load] over cfg.CatalogLibraries, with relative paths resolved against
// the directory of the config file they were declared in (RFC-001 §7).
// When no config file was loaded (cfg.Path is empty — built-in defaults,
// or libraries supplied entirely by flag overrides), relative paths
// resolve against the current directory.
func FromConfig(cfg *engineconfig.Config) (map[string]*Catalog, error) {
	baseDir := ""
	if cfg.Path != "" {
		baseDir = filepath.Dir(cfg.Path)
	}
	return Load(baseDir, cfg.CatalogLibraries)
}

// parseFile reads and parses one catalog library file. Any failure —
// unreadable file, malformed text format, unknown fields — is a hard
// error; there is no fallback and no partial load.
func parseFile(path string) (*catalogv1.Catalog, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the caller's engine configuration.
	if err != nil {
		return nil, fmt.Errorf("catalogs: %w", err)
	}
	msg := new(catalogv1.Catalog)
	if err := prototext.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("catalogs: %s: %w", path, err)
	}
	return msg, nil
}
