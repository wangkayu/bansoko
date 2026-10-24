package application

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
)

// Execution-attempt read projections. History is read separately from
// the marker view so the attempt list stays paginatable and never nests
// unbounded evidence into the acquisition response.

const (
	oneShotAttemptCursorVersion = "1"
	oneShotAttemptDefaultLimit  = 20
	oneShotAttemptMaxLimit      = 100
)

// OneShotAttemptSummary is one row of the attempts list: the bounded evidence
// an operator needs to pick a failed attempt to recover.
type OneShotAttemptSummary struct {
	ID                string
	Sequence          int64
	PreviousAttemptID *string
	Mode              string
	ResumeStage       *string
	State             string
	InputDigest       string
	CreatedAt         time.Time
	TerminalAt        *time.Time
	FailedStage       *string
	ReasonCode        *string
}

// OneShotAttemptPage is one sequence-DESC page of execution attempts.
type OneShotAttemptPage struct {
	Items      []OneShotAttemptSummary
	NextCursor *string
}

// OneShotExecutionAttempt is the full redacted attempt detail. The input
// snapshot is the same redacted shape the acquisition view exposes: never
// lease tokens, fencing tokens, secrets, or private paths.
type OneShotExecutionAttempt struct {
	ID                string
	AcquisitionID     string
	Sequence          int64
	PreviousAttemptID *string
	Mode              string
	ResumeStage       *string
	State             string
	InputDigest       string
	InputSnapshot     *OneShotInputSnapshot
	FailedStage       *string
	OutcomeCode       *string
	OutcomeReason     *string
	TerminalAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ListOneShotExecutionAttempts answers one stable sequence-DESC page. The
// acquisition must exist; a missing one is a typed 404 rather than an empty
// page, so a stale client cannot mistake a purged acquisition for "no
// attempts".
func (s *Service) ListOneShotExecutionAttempts(ctx context.Context, oneShotID string, limit int, cursor string) (OneShotAttemptPage, error) {
	if s == nil || s.oneShotMarkers == nil {
		return OneShotAttemptPage{}, errors.New("one-shot marker repository is not configured")
	}
	if limit == 0 {
		limit = oneShotAttemptDefaultLimit
	}
	if limit < 1 || limit > oneShotAttemptMaxLimit {
		return OneShotAttemptPage{}, &apperrors.BadRequest{Code: "LIMIT_INVALID", Message: "limit must be between 1 and 100."}
	}
	if _, err := s.requireOneShotAcquisition(ctx, oneShotID); err != nil {
		return OneShotAttemptPage{}, err
	}
	var before *int64
	if strings.TrimSpace(cursor) != "" {
		sequence, err := decodeOneShotAttemptCursor(cursor)
		if err != nil {
			return OneShotAttemptPage{}, &apperrors.BadRequest{Code: "INVALID_CURSOR", Message: "The pagination cursor is malformed or stale."}
		}
		before = &sequence
	}
	records, err := s.oneShotMarkers.ListAttemptsByAcquisition(ctx, oneShotID, before, limit+1)
	if err != nil {
		return OneShotAttemptPage{}, err
	}
	page := OneShotAttemptPage{Items: make([]OneShotAttemptSummary, 0, min(len(records), limit))}
	if len(records) > limit {
		records = records[:limit]
		next := encodeOneShotAttemptCursor(records[len(records)-1].Sequence)
		page.NextCursor = &next
	}
	for _, record := range records {
		page.Items = append(page.Items, OneShotAttemptSummary{
			ID:                record.ID,
			Sequence:          record.Sequence,
			PreviousAttemptID: record.PreviousAttempt,
			Mode:              string(record.Mode),
			ResumeStage:       oneShotStageString(record.ResumeStage),
			State:             record.State,
			InputDigest:       record.InputDigest,
			CreatedAt:         record.CreatedAt,
			TerminalAt:        record.TerminalAt,
			FailedStage:       oneShotStageString(record.FailedStage),
			ReasonCode:        record.OutcomeCode,
		})
	}
	return page, nil
}

// GetOneShotExecutionAttempt answers the full redacted attempt detail. The
// attempt must belong to the acquisition: a cross-acquisition id is a typed
// 404, never another acquisition's evidence.
func (s *Service) GetOneShotExecutionAttempt(ctx context.Context, oneShotID, attemptID string) (OneShotExecutionAttempt, error) {
	if s == nil || s.oneShotMarkers == nil {
		return OneShotExecutionAttempt{}, errors.New("one-shot marker repository is not configured")
	}
	if _, err := s.requireOneShotAcquisition(ctx, oneShotID); err != nil {
		return OneShotExecutionAttempt{}, err
	}
	record, err := s.oneShotMarkers.GetAttemptForAcquisition(ctx, oneShotID, attemptID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OneShotExecutionAttempt{}, &apperrors.NotFound{Resource: "one_shot_execution_attempt", ID: attemptID}
		}
		return OneShotExecutionAttempt{}, err
	}
	return OneShotExecutionAttempt{
		ID:                record.ID,
		AcquisitionID:     record.AcquisitionID,
		Sequence:          record.Sequence,
		PreviousAttemptID: record.PreviousAttempt,
		Mode:              string(record.Mode),
		ResumeStage:       oneShotStageString(record.ResumeStage),
		State:             record.State,
		InputDigest:       record.InputDigest,
		InputSnapshot:     decodeOneShotInputSnapshot(record.InputSnapshot),
		FailedStage:       oneShotStageString(record.FailedStage),
		OutcomeCode:       record.OutcomeCode,
		OutcomeReason:     record.OutcomeReason,
		TerminalAt:        record.TerminalAt,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}, nil
}

func (s *Service) requireOneShotAcquisition(ctx context.Context, oneShotID string) (*animeport.OneShotMarkerRecord, error) {
	if strings.TrimSpace(oneShotID) == "" {
		return nil, &apperrors.BadRequest{Code: "ONE_SHOT_ID_REQUIRED", Message: "The one-shot acquisition id is required."}
	}
	record, err := s.oneShotMarkers.GetByID(ctx, oneShotID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &apperrors.NotFound{Resource: "one_shot_acquisition", ID: oneShotID}
		}
		return nil, err
	}
	return record, nil
}

func oneShotStageString(stage *animeport.OneShotAttemptStage) *string {
	if stage == nil {
		return nil
	}
	value := string(*stage)
	return &value
}

// encodeOneShotAttemptCursor encodes the keyset position (the last sequence of
// the page) as an opaque versioned base64 string.
func encodeOneShotAttemptCursor(sequence int64) string {
	raw := oneShotAttemptCursorVersion + "|" + strconv.FormatInt(sequence, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeOneShotAttemptCursor(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("base64 decode: %w", err)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 || parts[0] != oneShotAttemptCursorVersion {
		return 0, errors.New("malformed or stale cursor")
	}
	sequence, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || sequence < 1 {
		return 0, errors.New("malformed cursor sequence")
	}
	return sequence, nil
}
