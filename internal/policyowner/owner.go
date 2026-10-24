package policyowner

import (
	"context"
	"time"

	"bansoko/internal/persistence/txscope"
)

// Default is metadata for a policy's creation template.
type Default[T any] struct {
	Policy    T
	Version   int64
	UpdatedAt time.Time
}

// Instance is metadata for a frozen per-instance policy snapshot.
type Instance[T any] struct {
	ManagedAnimeID       string
	Policy               T
	SourceDefaultVersion int64
	Version              int64
	UpdatedAt            time.Time
}

// Metadata describes policy lifecycle differences without implementing storage.
type Metadata struct {
	Kind                        string
	DefaultScope                string
	InstanceVersionFloor        int64
	SourceDefaultVersionFloor   int64
	MissingDefaultSourceVersion int64
	AuditRequired               bool
	MutationHook                string
}

// Capability binds policy-specific validation to shared lifecycle metadata.
type Capability[T any] struct {
	Metadata Metadata
	Validate func(T) error
}

// ValidateValue applies the policy-specific validator when one is provided.
func (c Capability[T]) ValidateValue(value T) error {
	if c.Validate == nil {
		return nil
	}
	return c.Validate(value)
}

// SnapshotPort copies one creation default into a caller-owned transaction.
type SnapshotPort interface {
	CopyDefaultToInstanceTx(context.Context, txscope.Tx, string) error
}

// DefaultSave describes a versioned default mutation. Actor and Channel are
// optional for owners without an audit stream.
type DefaultSave[T any] struct {
	ExpectedVersion int64
	Value           T
	Actor           string
	Channel         string
}

// InstanceSave describes a versioned frozen-instance mutation.
type InstanceSave[T any] struct {
	ManagedAnimeID  string
	ExpectedVersion int64
	Value           T
	Actor           string
	Channel         string
}

// Lifecycle is the common typed policy-owner surface. Implementations retain
// their own persistence, validation, audit, and mutation-specific semantics.
type Lifecycle[T any] interface {
	Capability() Capability[T]
	ReadDefault(context.Context) (*Default[T], error)
	WriteDefault(context.Context, DefaultSave[T]) (Default[T], error)
	ReadInstance(context.Context, string) (*Instance[T], error)
	WriteInstance(context.Context, InstanceSave[T]) (Instance[T], error)
	SnapshotPort
}
