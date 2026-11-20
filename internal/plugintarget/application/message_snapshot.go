package application

import (
	"encoding/json"
	"errors"
	"strings"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

const messageSnapshotVersion = 1

// messageSnapshot is the durable, channel-neutral output of a single render.
// The body and its links are projections of Parts, so they cannot diverge.
// Buttons is an optional v1 extension. Older snapshots have no buttons.
type messageSnapshot struct {
	Version int                     `json:"version"`
	Title   string                  `json:"title"`
	Parts   []notification.BodyPart `json:"parts"`
	Image   *imageInstruction       `json:"image,omitempty"`
	Buttons []notification.Button   `json:"buttons,omitempty"`
}

type imageInstruction struct {
	Source   string `json:"source,omitempty"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"catalog_provider,omitempty"`
	EntryRef string `json:"catalog_entry_ref,omitempty"`
	BGMID    string `json:"bgm_id,omitempty"`
	TMDBID   string `json:"tmdb_id,omitempty"`
	TMDBKind string `json:"tmdb_kind,omitempty"`
}

func freezeNotificationMessage(message notification.Message, facts map[string]any, sourceEvidence json.RawMessage) (json.RawMessage, error) {
	if !message.Valid() {
		return nil, errors.New("cannot freeze an invalid notification message")
	}
	snapshot := messageSnapshot{Version: messageSnapshotVersion, Title: message.Title, Parts: message.BodyParts, Buttons: message.Attachments.Buttons}
	if snapshot.Parts == nil {
		snapshot.Parts = []notification.BodyPart{}
	}
	if attachments := message.Attachments; attachments.Image {
		var evidence struct {
			Catalog struct {
				Provider string `json:"provider"`
				EntryRef string `json:"entry_ref"`
			} `json:"image_catalog_entry"`
			Binding struct {
				BGMID    string `json:"bgm_id"`
				TMDBID   string `json:"tmdb_id"`
				TMDBKind string `json:"tmdb_kind"`
			} `json:"image_binding"`
		}
		if len(sourceEvidence) != 0 {
			if err := json.Unmarshal(sourceEvidence, &evidence); err != nil {
				return nil, err
			}
		}
		image := &imageInstruction{
			Source: attachments.ImageSource, URL: attachments.ImageURL,
			Provider: evidence.Catalog.Provider, EntryRef: evidence.Catalog.EntryRef,
			BGMID: notificationFactString(facts, "bgm.id"), TMDBID: notificationFactString(facts, "tmdb.id"), TMDBKind: notificationFactString(facts, "tmdb.kind"),
		}
		if image.Source == "custom" {
			snapshot.Image = &imageInstruction{Source: image.Source, URL: image.URL}
		} else {
			if image.BGMID == "" {
				image.BGMID = evidence.Binding.BGMID
			}
			if image.TMDBID == "" {
				image.TMDBID, image.TMDBKind = evidence.Binding.TMDBID, evidence.Binding.TMDBKind
			}
			snapshot.Image = image
		}
	}
	return json.Marshal(snapshot)
}

func loadNotificationMessage(raw json.RawMessage, title, body, digest string) (messageSnapshot, error) {
	var snapshot messageSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return messageSnapshot{}, err
	}
	if snapshot.Version != messageSnapshotVersion || snapshot.Parts == nil ||
		snapshot.Title != title || notification.PlainText(snapshot.Parts) != body || hashBytes([]byte(title+"\n\n"+body)) != digest {
		return messageSnapshot{}, errors.New("notification message snapshot does not match its rendered text")
	}
	return snapshot, nil
}

func (i imageInstruction) request() plugintargetport.NotificationImageRequest {
	return plugintargetport.NotificationImageRequest{
		PreferredSource: i.Source, CatalogEntry: plugintargetport.NotificationCatalogEntry{Provider: i.Provider, EntryRef: i.EntryRef},
		BGMID: i.BGMID, TMDBID: i.TMDBID, TMDBKind: i.TMDBKind,
	}
}
