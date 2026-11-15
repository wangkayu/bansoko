// Package localupdate connects the container-local update command to the one
// updater already running inside the serving process. It never opens a database,
// writes update state, or exposes another HTTP/agent authentication surface.
package localupdate

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
	updateapplication "bansoko/internal/update/application"
)

const (
	protocolVersion = 1
	socketFileName  = "update-control.sock"
)

// Service is the existing embedded updater port. The composition root passes
// the exact same service instance to HTTP and to this local listener.
type Service interface {
	CheckUpdate(ctx context.Context) (updateapplication.Check, error)
	AdmitUpdate(ctx context.Context, input updateapplication.AdmitInput, idempotencyKey string) (updateapplication.Status, bool, error)
	GetUpdateStatus(ctx context.Context) (updateapplication.Status, bool, error)
}

type request struct {
	Version        int    `json:"version"`
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type response struct {
	Check  *generated.UpdateCheck          `json:"check,omitempty"`
	Status *generated.EmbeddedUpdateStatus `json:"status,omitempty"`
	Error  *responseError                  `json:"error,omitempty"`
}

type responseError struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
