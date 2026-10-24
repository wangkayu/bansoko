package application

import (
	"encoding/json"

	protocol "bansoko/internal/acquisition/terminal/domain"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// Build notification facts exclusively from the accepted input, immutable
// Artifact, the release's frozen source facts and persistent publication
// proof. Current management rows are absent.
// sourceFacts is the release's frozen source_facts (rt, anibt); render is the
// operation's frozen render snapshot (episode titles).
func successfulFileNotificationPayload(input json.RawMessage, file artifactFileContext, fact, checkpoint protocol.Value, collision string, sourceFacts, render []byte) (json.RawMessage, error) {
	accepted, err := protocol.ParseStrict(input)
	if err != nil {
		return nil, err
	}
	text := func(v protocol.Value) string {
		if v.Object["state"].String == "known" {
			return v.Object["value"].String
		}
		return ""
	}
	source := accepted.Object["source"]
	binding := fact.Object["render_inputs"].Object["binding"]
	title := ""
	if binding.Object["state"].String == "known" {
		title = text(binding.Object["value"].Object["effective_title"])
	}
	fields := fileOperationNotificationFacts{animeTitle: title, releaseTitle: text(source.Object["title"]), filePath: file.relativePath, fileSize: file.sizeBytes, fileMediaKind: file.mediaKind, fileFacts: file.facts, operation: accepted.Object["delivery"].Object["operation"].String, collision: collision}
	if source.Object["release"].Object["state"].String == "known" {
		fields.releaseID = source.Object["release"].Object["value"].Object["ref"].String
	}
	// The typed path reads no management rows: anime.id and the other shared
	// facts come from the accepted input, ep.* from the file fact. The
	// SQL projector file_success_notification merges the same facts.
	fields.frozen = notification.InputFacts(protocol.Canonical(accepted))
	decoded, _ := notification.DecodeFacts(protocol.Canonical(fact))
	coverage, _ := decoded["identity_coverage"].(map[string]any)
	for key, value := range notification.EpisodeFacts(coverage["identity"]) {
		fields.frozen[key] = value
	}
	for key, value := range notification.ValueFacts(notification.InputVarsOutput(protocol.Canonical(accepted)), sourceFacts, notificationText) {
		fields.frozen[key] = value
	}
	for key, value := range notification.TitleFacts(protocol.Canonical(accepted), notificationText) {
		fields.frozen[key] = value
	}
	// The render is the fact's only producer: the notification never queries
	// the store, it reads the frozen episode_titles block the whitelisted
	// render landed (absent while the lookup was down — the notification is
	// then emitted without the keys).
	for key, value := range notification.EpisodeTitleFacts(render, notificationText) {
		fields.frozen[key] = value
	}
	target := checkpoint.Object["publish_target"].Object["proof"].Object["target"].Object["location"]
	fields.locationRoot = target.Object["root"].String
	fields.targetPath = target.Object["canonical_path"].String
	return buildFileOperationNotificationPayload(fields, plugintargetport.TriggerFileOperationCompleted, "success", "file_operation_completed", "")
}
