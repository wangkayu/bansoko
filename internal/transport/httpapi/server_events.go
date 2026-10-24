package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"bansoko/internal/events"
	"bansoko/internal/transport/httpapi/generated"
)

const eventStreamHeartbeat = 20 * time.Second

// eventStreamWriteDeadline is refreshed after every successful write. The
// server's normal WriteTimeout is finite for ordinary API requests, while an
// SSE connection is intentionally long-lived and remains safe because it
// emits a heartbeat before this deadline expires.
const eventStreamWriteDeadline = 2 * time.Minute

// StreamEvents keeps one authenticated connection open and sends only
// read-model invalidation signals. The generated contract intentionally does
// not model the streaming body; this method is registered by the generated
// route and owns the SSE wire format.
func (s *Server) StreamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, serviceNotConfigured("streaming response"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	subscriber, cancel := s.deps.Events.Subscribe()
	defer cancel()

	// Send an initial comment so fetch-based clients receive headers and can
	// report the connection as established without waiting for a business event.
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()
	refreshSSEWriteDeadline(w)

	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-subscriber:
			if !open {
				return
			}
			if err := writeSSEEvent(w, flusher, event); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
			refreshSSEWriteDeadline(w)
		}
	}
}

// realtimeEventPayload maps a hub signal onto the contract's RealtimeEvent;
// an empty ManagedAnimeID stays absent so unscoped signals keep their shape.
func realtimeEventPayload(event events.Event) generated.RealtimeEvent {
	payload := generated.RealtimeEvent{Target: generated.RealtimeEventTarget(event.Target)}
	if event.ManagedAnimeID != "" {
		id := event.ManagedAnimeID
		payload.ManagedAnimeId = &id
	}
	return payload
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event events.Event) error {
	payload, err := json.Marshal(realtimeEventPayload(event))
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	flusher.Flush()
	refreshSSEWriteDeadline(w)
	return nil
}

func refreshSSEWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(eventStreamWriteDeadline))
}
