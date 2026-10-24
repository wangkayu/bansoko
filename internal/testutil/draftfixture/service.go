// Package draftfixture exposes draft setup for tests outside the draft owner.
package draftfixture

import (
	draft "bansoko/internal/anime/draft/application"
	"database/sql"
)

type Service = draft.Service
type Dependencies = draft.Dependencies
type CandidateDraftInput = draft.CandidateDraftInput
type CandidateSubscribeInput = draft.CandidateSubscribeInput
type SubscribeResult = draft.SubscribeResult
type AnimeConfigCandidate = draft.AnimeConfigCandidate

func NewService(db *sql.DB, deps Dependencies) (*Service, error) { return draft.NewService(db, deps) }
