//go:build !linux && !darwin

package filesystem

import (
	"context"
	"errors"

	"bansoko/internal/library/domain"
)

var errPublicationUnsupported = errors.New("typed publication is unsupported on this platform")

type publicationSyscalls struct{}

func defaultPublicationSyscalls() publicationSyscalls           { return publicationSyscalls{} }
func (s publicationSyscalls) withDefaults() publicationSyscalls { return s }
func (s publicationSyscalls) withRenameHook(func(string, string) error) publicationSyscalls {
	return s
}
func (OSFileExecutor) PreparePublication(context.Context, domain.FileOperation, string) (domain.PublicationPreparation, error) {
	return domain.PublicationPreparation{}, errPublicationUnsupported
}
func (*PublicationInvocation) ExecuteHardlink(context.Context, domain.FileOperation, domain.PublicationPreparation, domain.TypedPublicationCallbacks) error {
	return errPublicationUnsupported
}
func (*PublicationInvocation) ExecuteCopy(context.Context, domain.FileOperation, domain.PublicationPreparation, domain.TypedPublicationCallbacks) error {
	return errPublicationUnsupported
}
func (OSFileObserver) ObservePath(context.Context, string) (domain.PathObservation, error) {
	return domain.PathObservation{}, errPublicationUnsupported
}
func (OSFileObserver) SameDevice(context.Context, string, string) (bool, error) {
	return false, errPublicationUnsupported
}
