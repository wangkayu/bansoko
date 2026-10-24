package filesystem

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"

	"bansoko/internal/library/domain"
)

var publicationBoot struct {
	once  sync.Once
	nonce string
	err   error
}

func publicationNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// PublicationInvocation is owned by one adapter call. Its consumed bit cannot
// be reset by rereading a database permit. There are no detached tasks/retries.
type PublicationInvocation struct {
	session       domain.PublicationSession
	consumed      atomic.Bool
	cleanupProbe  cleanupMutationProbe
	beforePublish func(temp, final string) error
	sys           publicationSyscalls
}

func (executor OSFileExecutor) NewPublicationInvocation() (*PublicationInvocation, error) {
	publicationBoot.once.Do(func() { publicationBoot.nonce, publicationBoot.err = publicationNonce() })
	if publicationBoot.err != nil {
		return nil, publicationBoot.err
	}
	call, err := publicationNonce()
	if err != nil {
		return nil, err
	}
	sys := defaultPublicationSyscalls()
	if executor.publicationSys != nil {
		sys = executor.publicationSys.withDefaults()
	}
	return &PublicationInvocation{session: domain.PublicationSession{ExecutorSession: publicationBoot.nonce + ":" + call, BootNonce: publicationBoot.nonce, CallNonce: call}, cleanupProbe: executor.Hooks.PublicationCleanupPoint, beforePublish: executor.Hooks.BeforePublish, sys: sys}, nil
}

func (i *PublicationInvocation) Session() domain.PublicationSession {
	if i == nil {
		return domain.PublicationSession{}
	}
	return i.session
}
