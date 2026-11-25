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
func successfulFileNotificationPayload(input json.RawMessage, file artifactFileContext, fact, checkpoint protocol.Value, collision, entryKind string, sourceFacts, render, fileIdentity, releaseIdentity []byte) (json.RawMessage, error) {
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
	fields.frozen["task.origin"] = notification.TaskOrigin(entryKind)
	for key, value := range notification.MediaFileEpisodeFacts(fileIdentity, releaseIdentity) {
		fields.frozen[key] = value
	}
	for key, value := range notification.ValueFacts(sourceFacts, notificationText) {
		fields.frozen[key] = value
	}
	for key, value := range notification.TitleFacts(protocol.Canonical(accepted), notificationText) {
		fields.frozen[key] = value
	}
	// Project titles already frozen by a whitelisted naming render. If the
	// notification target also references a title this block withheld, its
	// admission resolver performs the independent cache-first lookup before
	// freezing the delivery; neither this producer nor delivery rendering queries.
	for key, value := range notification.EpisodeTitleFacts(render, notificationText) {
		fields.frozen[key] = value
	}
	// frt.* is the frozen parse of the original download file name: the same
	// block naming rendered with, never the release title and never a parse of
	// the output name.
	for key, value := range notification.FileReltitleFacts(render, "frt", notificationText) {
		fields.frozen[key] = value
	}
	target := checkpoint.Object["publish_target"].Object["proof"].Object["target"].Object["location"]
	fields.targetPath = target.Object["canonical_path"].String
	return buildFileOperationNotificationPayload(fields, plugintargetport.TriggerFileOperationCompleted, "success", "file_operation_completed", "")
}
