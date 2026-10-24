package domain

import (
	"context"
	"time"
)

// PublicationSession identifies one synchronous adapter invocation. The adapter
// owns nonce generation and single-use enforcement; claims cannot construct a
// second executor invocation by reading a previously issued database grant.
type PublicationSession struct {
	ExecutorSession string
	BootNonce       string
	CallNonce       string
}

type PublicationStepReceipt struct {
	Step             string
	Source           PathObservation
	Target           PathObservation
	Parent           FileIdentity
	TargetPath       string
	CandidateOrdinal int64
	Displaced        PathObservation
	ObservedAt       time.Time
	Err              error
}

type PublicationJoin struct {
	Session         PublicationSession
	CoveredSteps    []string
	JoinedAt        time.Time
	ObserverVersion string
}

// TypedPublicationCallbacks are internal capabilities wired by the owner.
// Each grant callback must commit before returning true. Receipt failure stops
// subsequent mutations. Joined is called only after actual synchronous return.
type TypedPublicationCallbacks struct {
	StartDispatch func(context.Context, PublicationSession) (bool, error)
	StartStep     func(context.Context, PublicationSession, string) (bool, error)
	// CheckMutationDeadline checks the committed grant's local monotonic budget.
	CheckMutationDeadline func(context.Context, PublicationSession, string) error
	RecordStep            func(context.Context, PublicationSession, PublicationStepReceipt) error
	Joined                func(context.Context, PublicationJoin) error
}
