//go:build !darwin && !linux

package application

const (
	payloadNoFollowFlag      = 0
	payloadNoFollowSupported = false
)
