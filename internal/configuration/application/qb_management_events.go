package application

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"bansoko/internal/apperrors"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/events"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

func (s QBManagementService) unconfirmedQBDeletion(ctx context.Context, connectionID string, torrent qbittorrent.TorrentInfo, deleteFiles bool) error {
	s.recordQBDeletion(ctx, connectionID, torrent, deleteFiles, observabilityport.MessageQBDeletionUnconfirmed)
	return &apperrors.BadRequest{Code: "QB_DELETION_UNCONFIRMED", Message: "Could not confirm whether the download task was deleted."}
}

func (s QBManagementService) recordQBDeletion(ctx context.Context, connectionID string, torrent qbittorrent.TorrentInfo, deleteFiles bool, code observabilityport.MessageCode) {
	if s.activityWriter == nil {
		return
	}
	// The downloader effect cannot be rolled back. Give its evidence a short
	// write window even if the initiating HTTP request has been cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	outcome := "failure"
	if code == observabilityport.MessageQBTaskDeleted {
		outcome = "success"
	}
	details := json.RawMessage(`{"data_delete_requested":false}`)
	if deleteFiles {
		details = json.RawMessage(`{"data_delete_requested":true}`)
	}
	operationID := uuid.NewString()
	record := observabilityport.ActivityRecord{
		Kind: string(observabilityport.ActivityKindQBTaskDeleted), Outcome: outcome,
		ReasonCode: string(code), MessageCode: code, Details: details,
		SubjectType: "qb_torrent", SubjectID: strings.ToLower(torrent.Hash), SubjectVersion: 1,
		SubjectDisplay: torrent.Name, CausationType: "qb_connection", CausationID: connectionID,
		CorrelationID: operationID, DedupeKey: "qb_task_deleted:" + operationID,
	}
	tx, err := s.owner.db.BeginTx(ctx, nil)
	if err == nil {
		defer tx.Rollback()
		err = s.activityWriter.AppendTx(ctx, txscope.Wrap(tx), record)
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message: "Could not persist the downloader deletion result.", Component: "qb_management",
				Operation: "delete_task", Outcome: "failure", ReasonCode: "qb_delete_event_write_failed",
				OwnerType: "connection", OwnerID: connectionID,
			})
		}
		return
	}
	s.eventHub.Publish(events.Event{Target: events.TargetRecords})
}
