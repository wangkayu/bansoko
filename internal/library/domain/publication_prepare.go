package domain

import "context"

// PublicationPreparation is a read-only capture. It grants no mutation and
// creates neither a temp file nor a target directory.
type PublicationPreparation struct {
	Source         PathObservation
	SourceParent   FileIdentity
	TargetParent   FileIdentity
	TempPath       string
	ExpectedTarget *PathObservation
}

type PublicationPreparer interface {
	PreparePublication(context.Context, FileOperation, string) (PublicationPreparation, error)
}
