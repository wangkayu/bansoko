package httpapi

import (
	"net/http"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/transport/httpapi/generated"
)

// notificationTriggerKindVocabularyView maps the closed plugintarget trigger
// registry to the published vocabulary of the notifications trigger-kinds row.
// Reading the package registry directly keeps the contract answer and the
// registry one and the same.
func notificationTriggerKindVocabularyView() generated.NotificationTriggerKindVocabulary {
	kinds := make([]generated.NotificationTriggerKind, 0, len(plugintargetport.Triggers))
	for _, trigger := range plugintargetport.Triggers {
		kinds = append(kinds, generated.NotificationTriggerKind(trigger))
	}
	return generated.NotificationTriggerKindVocabulary{TriggerKinds: kinds}
}

func (s *Server) ListNotificationTriggerKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r.Context(), http.StatusOK, notificationTriggerKindVocabularyView())
}
