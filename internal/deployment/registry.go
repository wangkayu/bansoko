package deployment

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultRequiredKeys are the core owner reconcilers every recovery run must
// register. Production-enabled FileMaintenance effect owners (Alignment
// Operation, Cleanup deletion) add their own registrations in their
// P3 children; the gate enumerates them through Enabled() at run time.
var DefaultRequiredKeys = []string{
	"qb_download_task",
	"artifact",
	"file_operation",
}

// Registry is the composition-time typed wiring. It is built once by the
// composition root; runtime enumeration is read-only.
type Registry struct {
	required map[string]bool
	byKey    map[string]Registration
	order    []string
}

// NewRegistry validates the wiring and fails closed on duplicate keys,
// missing required keys, disabled-but-required registrations, handlers that
// are nil while enabled, or unsupported versions.
func NewRegistry(required []string, registrations ...Registration) (*Registry, error) {
	registry := &Registry{
		required: map[string]bool{},
		byKey:    map[string]Registration{},
	}
	for _, key := range required {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("registry required key must not be empty")
		}
		if registry.required[key] {
			return nil, fmt.Errorf("registry required key %q duplicated", key)
		}
		registry.required[key] = true
	}
	for _, reg := range registrations {
		key := strings.TrimSpace(reg.Key)
		if key == "" {
			return nil, fmt.Errorf("registry registration key must not be empty")
		}
		if _, exists := registry.byKey[key]; exists {
			return nil, fmt.Errorf("registry registration key %q duplicated", key)
		}
		if reg.Version < 1 {
			return nil, fmt.Errorf("registry registration %q must declare a supported version", key)
		}
		if reg.Effect.Owner == "" || reg.Effect.Effect == "" {
			return nil, fmt.Errorf("registry registration %q must declare owner and effect identity", key)
		}
		if reg.Handler == nil {
			return nil, fmt.Errorf("registry registration %q has no handler", key)
		}
		if reg.Enabled == nil {
			reg.Enabled = func() bool { return true }
		}
		registry.byKey[key] = reg
		registry.order = append(registry.order, key)
	}
	for key := range registry.required {
		reg, ok := registry.byKey[key]
		if !ok {
			return nil, fmt.Errorf("registry is missing required registration %q", key)
		}
		if reg.Enabled != nil && !reg.Enabled() {
			return nil, fmt.Errorf("registry required registration %q must be enabled", key)
		}
	}
	sort.Strings(registry.order)
	return registry, nil
}

// EnabledEntries returns the registrations that must run for this recovery
// window: all required keys plus every registration whose enabled predicate
// is true. Duplicates are impossible by construction; an enabled entry whose
// key is also required appears once.
func (r *Registry) EnabledEntries() []Registration {
	var entries []Registration
	for _, key := range r.order {
		reg := r.byKey[key]
		if r.required[key] || reg.Enabled() {
			entries = append(entries, reg)
		}
	}
	return entries
}

// Inventory returns the versioned registry facts for the checkpoint and the
// machine-readable output.
func (r *Registry) Inventory() []InventoryEntry {
	var entries []InventoryEntry
	for _, key := range r.order {
		reg := r.byKey[key]
		entries = append(entries, InventoryEntry{
			Key:     reg.Key,
			Owner:   reg.Effect.Owner,
			Effect:  reg.Effect.Effect,
			Version: reg.Version,
			Enabled: r.required[key] || reg.Enabled(),
		})
	}
	return entries
}

// RequiredKeys exposes the required set (used by tests and the CLI).
func (r *Registry) RequiredKeys() []string {
	var keys []string
	for key := range r.required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
