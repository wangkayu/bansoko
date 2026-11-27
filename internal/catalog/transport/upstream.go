package transport

import (
	"time"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func UpstreamStatus(value providerdomain.Status) *generated.UpstreamStatus {
	if value.State == "" || value.CheckedAt.IsZero() {
		return nil
	}
	status := &generated.UpstreamStatus{State: generated.UpstreamStatusState(value.State), CheckedAt: value.CheckedAt}
	if value.ReasonCode != "" {
		code := string(value.ReasonCode)
		status.ReasonCode = &code
	}
	if !value.CooldownUntil.IsZero() {
		status.CooldownUntil = &value.CooldownUntil
	}
	if value.RetryAfter > 0 || value.State == providerdomain.StateRateLimited || value.State == providerdomain.StateCooldown {
		seconds := int(value.RetryAfter / time.Second)
		if value.RetryAfter%time.Second > 0 {
			seconds++
		}
		status.RetryAfterSeconds = &seconds
	}
	return status
}

func SuccessfulUpstream(count int, checkedAt time.Time) *generated.UpstreamStatus {
	state := providerdomain.StateAvailable
	if count == 0 {
		state = providerdomain.StateEmpty
	}
	return UpstreamStatus(providerdomain.Status{State: state, CheckedAt: checkedAt})
}
