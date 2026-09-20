// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package contract

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// catalogFS embeds all built-in component contracts.
//
//go:embed catalog/*.yaml
var catalogFS embed.FS

// Catalog is a collection of contracts indexed by kind and name.
type Catalog struct {
	contracts []Contract
	byKind    map[Kind]map[string]Contract
}

// NewCatalog builds a catalog from the given contracts.
func NewCatalog(contracts []Contract) (*Catalog, error) {
	c := &Catalog{byKind: map[Kind]map[string]Contract{}}
	for _, contractItem := range contracts {
		if err := Validate(contractItem); err != nil {
			return nil, err
		}
		if c.byKind[contractItem.Kind] == nil {
			c.byKind[contractItem.Kind] = map[string]Contract{}
		}
		c.byKind[contractItem.Kind][contractItem.Name] = contractItem
		c.contracts = append(c.contracts, contractItem)
	}
	return c, nil
}

// LoadEmbeddedCatalog loads the contracts shipped inside the CLI binary.
func LoadEmbeddedCatalog() (*Catalog, error) {
	entries, err := fs.ReadDir(catalogFS, "catalog")
	if err != nil {
		return nil, fmt.Errorf("read embedded contract catalog: %w", err)
	}

	var contracts []Contract
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(catalogFS, filepath.ToSlash(filepath.Join("catalog", entry.Name())))
		if err != nil {
			return nil, fmt.Errorf("read embedded contract %q: %w", entry.Name(), err)
		}
		parsed, err := ParseDocuments(data)
		if err != nil {
			return nil, fmt.Errorf("parse embedded contract %q: %w", entry.Name(), err)
		}
		contracts = append(contracts, parsed...)
	}

	return NewCatalog(contracts)
}

// ParseDocuments parses one or more YAML documents into contracts. Empty
// documents are ignored.
func ParseDocuments(data []byte) ([]Contract, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var contracts []Contract
	for {
		var contractItem Contract
		err := decoder.Decode(&contractItem)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// Skip empty documents between document separators.
		if contractItem.APIVersion == "" && contractItem.Name == "" {
			continue
		}
		contracts = append(contracts, contractItem)
	}
	return contracts, nil
}

// LoadFromDir discovers cgapp.contract.yaml in the given directory and parses
// it. A missing file is not an error: it returns (nil, nil) so callers can
// fall back to loose mode.
func LoadFromDir(dir string) ([]Contract, error) {
	path := filepath.Join(dir, DiscoveredContractFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read discovered contract %q: %w", path, err)
	}
	contracts, err := ParseDocuments(data)
	if err != nil {
		return nil, fmt.Errorf("parse discovered contract %q: %w", path, err)
	}
	for _, contractItem := range contracts {
		if err := Validate(contractItem); err != nil {
			return nil, fmt.Errorf("validate discovered contract %q: %w", path, err)
		}
	}
	return contracts, nil
}

// Validate checks the structural validity of a contract.
func Validate(c Contract) error {
	if c.APIVersion != SchemaVersion {
		return fmt.Errorf("contract %q: unsupported apiVersion %q (want %q)", c.Name, c.APIVersion, SchemaVersion)
	}
	switch c.Kind {
	case KindBackend, KindFrontend, KindDatabase, KindCache, KindProxy, KindDeploy:
	default:
		return fmt.Errorf("contract %q: unknown kind %q", c.Name, c.Kind)
	}
	if c.Name == "" {
		return fmt.Errorf("contract of kind %q has an empty name", c.Kind)
	}
	for _, port := range c.Ports {
		if port.Name == "" {
			return fmt.Errorf("contract %q: port without a name", c.Name)
		}
		if port.Port <= 0 {
			return fmt.Errorf("contract %q: port %q has no container port", c.Name, port.Name)
		}
	}
	for _, route := range c.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			return fmt.Errorf("contract %q: route must start with '/', got %q", c.Name, route.Path)
		}
	}
	return nil
}

// All returns all contracts in the catalog.
func (c *Catalog) All() []Contract {
	return c.contracts
}

// ByName returns the contract of a kind/name pair.
func (c *Catalog) ByName(kind Kind, name string) (Contract, bool) {
	if group, ok := c.byKind[kind]; ok {
		contractItem, found := group[name]
		return contractItem, found
	}
	return Contract{}, false
}

// Merge overlays contracts on top of the receiver: same kind/name pairs are
// replaced, new pairs are added. It returns a new catalog and never mutates
// the receiver.
func (c *Catalog) Merge(others []Contract) (*Catalog, error) {
	merged := make([]Contract, 0, len(c.contracts)+len(others))
	index := map[Kind]map[string]int{}
	for i, contractItem := range c.contracts {
		merged = append(merged, contractItem)
		if index[contractItem.Kind] == nil {
			index[contractItem.Kind] = map[string]int{}
		}
		index[contractItem.Kind][contractItem.Name] = i
	}
	for _, other := range others {
		if err := Validate(other); err != nil {
			return nil, err
		}
		if group, ok := index[other.Kind]; ok {
			if pos, exists := group[other.Name]; exists {
				merged[pos] = other
				continue
			}
		}
		index[other.Kind][other.Name] = len(merged)
		merged = append(merged, other)
	}
	return NewCatalog(merged)
}
