//go:build darwin || linux

package application

import "syscall"

const (
	payloadNoFollowFlag      = syscall.O_NOFOLLOW
	payloadNoFollowSupported = true
)
