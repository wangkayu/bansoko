// Package readiness aggregates the runtime admission evidence for the
// server. /readyz consumes Checker.Report; deployment, CI, the P0.7 bundle
// builder and the P2 supervisor all consume the same machine-readable shape.
//
// Hard-not-ready failures (database, schema, identity, bootstrap, owner read,
// maintenance) make ready=false with a stable reason code. Optional external
// dependencies such as qBittorrent are reported as degraded separately and
// never downgrade the hard verdict on their own.
package readiness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	accessdomain "bansoko/internal/accesscontrol/domain"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/maintenance"
	"bansoko/internal/runtime/migrations"
)

// CheckOutcome is one evidence item.
type CheckOutcome struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Report is the machine-readable readiness snapshot.
type Report struct {
	Ready     bool                    `json:"ready"`
	Reason    string                  `json:"reason"`
	Checks    map[string]CheckOutcome `json:"checks"`
	CheckedAt time.Time               `json:"checked_at"`
	// DeploymentIdentity is the three-layer release identity of this process
	// (base image / selected overlay release / update operation). It is null
	// when the process was not launched through the update-aware bootstrap.
	DeploymentIdentity *deploymentidentity.Identity `json:"deployment_identity,omitempty"`
}

// Dependencies are the evidence providers. Nil entries are reported as
// "not_configured" (degraded) rather than hard failures, so a minimal server
// can still answer /readyz.
type Dependencies struct {
	DB         *sql.DB
	Migrations migrations.Runner
	Build      buildinfo.BuildInfo
	Bootstrap  BootstrapProvider
	Secrets    SecretProvider
	Workers    WorkerProvider
	Gate       *maintenance.Gate
	QB         QBProvider
	// Deployment proves the three-layer deployment identity (design §6).
	// Nil means the process was launched without update-aware bootstrap
	// identity (bare-metal dev): the check reports "not_provided" and never
	// hard-fails on its own.
	Deployment DeploymentIdentityProvider
}

// DeploymentIdentityProvider exposes the injected deployment identity plus one
// live read of the updates-volume current pointer for the consistency rules.
type DeploymentIdentityProvider interface {
	Identity() deploymentidentity.Identity
	CurrentPointer() deploymentidentity.PointerState
	Validate() error
}

// DeploymentIdentityRequirement is an optional stricter runtime contract used
// by production composition roots. Native development providers may omit it.
type DeploymentIdentityRequirement interface {
	RequireSource() bool
}

// BootstrapProvider exposes the instance bootstrap state.
type BootstrapProvider interface {
	Status(ctx context.Context) (accessdomain.BootstrapStatus, error)
}

// SecretProvider proves the secret encryption key can decrypt managed secrets.
type SecretProvider interface {
	Available(ctx context.Context) error
}

// WorkerProvider reports the registered worker lanes/kinds.
type WorkerProvider interface {
	AllowedKinds() []string
}

// QBProvider reports qBittorrent configuration health (degraded-only check).
type QBProvider interface {
	Healthy(ctx context.Context) (bool, string)
}

// Checker aggregates the readiness evidence.
type Checker struct {
	deps Dependencies
}

// New returns a Checker over the given dependencies.
func New(deps Dependencies) *Checker {
	return &Checker{deps: deps}
}

// Report computes the full readiness snapshot.
func (c *Checker) Report(ctx context.Context) Report {
	checks := map[string]CheckOutcome{}
	hardFailed := ""

	// 1. HTTP alive is implied by serving this request; record it as ok.
	checks["http_alive"] = CheckOutcome{OK: true, Code: "alive"}

	// 2. PostgreSQL connectivity.
	if c.deps.DB == nil {
		checks["postgres"] = CheckOutcome{OK: false, Code: "not_configured", Detail: "database not configured"}
		hardFailed = orReason(hardFailed, "postgres:not_configured")
	} else if err := c.deps.DB.PingContext(ctx); err != nil {
		checks["postgres"] = CheckOutcome{OK: false, Code: "unreachable", Detail: err.Error()}
		hardFailed = orReason(hardFailed, "postgres:unreachable")
	} else {
		checks["postgres"] = CheckOutcome{OK: true, Code: "ok"}
	}

	// 3. Schema target / migration checksum.
	if c.deps.DB == nil {
		checks["schema"] = CheckOutcome{OK: false, Code: "not_configured"}
		hardFailed = orReason(hardFailed, "schema:not_configured")
	} else if err := c.deps.Migrations.Verify(ctx); err != nil {
		checks["schema"] = CheckOutcome{OK: false, Code: "mismatch", Detail: err.Error()}
		hardFailed = orReason(hardFailed, "schema:mismatch")
	} else {
		checks["schema"] = CheckOutcome{OK: true, Code: "ok"}
	}

	// 4. Build/bundle identity.
	if c.deps.Build.Commit == "" || c.deps.Build.BuildID == "" || c.deps.Build.Commit == "unknown" {
		checks["build_identity"] = CheckOutcome{OK: false, Code: "missing", Detail: "build identity not populated"}
		hardFailed = orReason(hardFailed, "build_identity:missing")
	} else {
		checks["build_identity"] = CheckOutcome{OK: true, Code: "ok"}
	}

	// 5. Secret encryption availability.
	if c.deps.Secrets == nil {
		checks["secret_encryption"] = CheckOutcome{OK: false, Code: "not_configured"}
		hardFailed = orReason(hardFailed, "secret_encryption:not_configured")
	} else if err := c.deps.Secrets.Available(ctx); err != nil {
		checks["secret_encryption"] = CheckOutcome{OK: false, Code: "unavailable", Detail: err.Error()}
		hardFailed = orReason(hardFailed, "secret_encryption:unavailable")
	} else {
		checks["secret_encryption"] = CheckOutcome{OK: true, Code: "ok"}
	}

	// 6. Bootstrap state.
	if c.deps.Bootstrap == nil {
		checks["bootstrap"] = CheckOutcome{OK: false, Code: "not_configured"}
		hardFailed = orReason(hardFailed, "bootstrap:not_configured")
	} else {
		status, err := c.deps.Bootstrap.Status(ctx)
		if err != nil {
			checks["bootstrap"] = CheckOutcome{OK: false, Code: "unreadable", Detail: err.Error()}
			hardFailed = orReason(hardFailed, "bootstrap:unreadable")
		} else {
			checks["bootstrap"] = CheckOutcome{OK: true, Code: string(status.State)}
		}
	}

	// 7. Key owner read.
	if c.deps.DB == nil {
		checks["owner_read"] = CheckOutcome{OK: false, Code: "not_configured"}
		hardFailed = orReason(hardFailed, "owner_read:not_configured")
	} else {
		var one int
		err := c.deps.DB.QueryRowContext(ctx, `SELECT 1 FROM local_accounts LIMIT 1`).Scan(&one)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			checks["owner_read"] = CheckOutcome{OK: false, Code: "failed", Detail: err.Error()}
			hardFailed = orReason(hardFailed, "owner_read:failed")
		} else {
			checks["owner_read"] = CheckOutcome{OK: true, Code: "ok"}
		}
	}

	// 8. Worker lanes.
	if c.deps.Workers == nil {
		checks["worker_lanes"] = CheckOutcome{OK: false, Code: "not_configured"}
		hardFailed = orReason(hardFailed, "worker_lanes:not_configured")
	} else {
		kinds := c.deps.Workers.AllowedKinds()
		if len(kinds) == 0 {
			checks["worker_lanes"] = CheckOutcome{OK: false, Code: "empty", Detail: "no worker kinds registered"}
			hardFailed = orReason(hardFailed, "worker_lanes:empty")
		} else {
			checks["worker_lanes"] = CheckOutcome{OK: true, Code: "ok", Detail: fmt.Sprintf("%d kinds", len(kinds))}
		}
	}

	// 9. Maintenance gate.
	if c.deps.Gate != nil {
		status := c.deps.Gate.Status()
		checks["maintenance"] = CheckOutcome{OK: status.State != maintenance.Maintenance, Code: string(status.State)}
		if status.State == maintenance.Maintenance {
			hardFailed = orReason(hardFailed, "maintenance:active")
		}
		// The resume gate only gates recovery: a serving instance does not need
		// it; maintenance/draining do.
		resumePassed := status.State == maintenance.Serving || status.AutomationResumeGate
		checks["automation_resume_gate"] = CheckOutcome{OK: resumePassed, Code: resumeGateCode(status, resumePassed)}
		if !resumePassed {
			hardFailed = orReason(hardFailed, "automation_resume_gate:not_passed")
		}
	} else {
		checks["maintenance"] = CheckOutcome{OK: true, Code: "serving"}
		checks["automation_resume_gate"] = CheckOutcome{OK: true, Code: "passed"}
	}

	// 10. Deployment identity consistency (three-layer identity, design §6).
	// Only a provided identity can be inconsistent; absence is legitimate for
	// native development, while a production composition root may require the
	// source marker even when no individual identity fact was injected.
	var providedIdentity *deploymentidentity.Identity
	if c.deps.Deployment == nil {
		checks["deployment_identity"] = CheckOutcome{OK: true, Code: "not_provided"}
	} else {
		identity := c.deps.Deployment.Identity()
		if !identity.Provided() {
			required := false
			if requirement, ok := c.deps.Deployment.(DeploymentIdentityRequirement); ok {
				required = requirement.RequireSource()
			}
			if required {
				checks["deployment_identity"] = CheckOutcome{OK: false, Code: "unverified", Detail: deploymentidentity.ErrRuntimeSourceRequired.Error()}
				hardFailed = orReason(hardFailed, "deployment_identity:unverified")
			} else {
				checks["deployment_identity"] = CheckOutcome{OK: true, Code: "not_provided"}
			}
		} else {
			shown := identity
			shown.ProcessBuildID = c.deps.Build.BuildID
			providedIdentity = &shown
			pointer := c.deps.Deployment.CurrentPointer()
			runtimeValidationErr := error(nil)
			if requirement, ok := c.deps.Deployment.(DeploymentIdentityRequirement); ok {
				runtimeValidationErr = shown.ValidateForRuntime(&pointer, requirement.RequireSource())
			}
			if runtimeValidationErr != nil {
				checks["deployment_identity"] = CheckOutcome{OK: false, Code: "unverified", Detail: runtimeValidationErr.Error()}
				hardFailed = orReason(hardFailed, "deployment_identity:unverified")
			} else if err := c.deps.Deployment.Validate(); err != nil {
				checks["deployment_identity"] = CheckOutcome{OK: false, Code: "unverified", Detail: err.Error()}
				hardFailed = orReason(hardFailed, "deployment_identity:unverified")
			} else if err := shown.Validate(&pointer); err != nil {
				checks["deployment_identity"] = CheckOutcome{OK: false, Code: "inconsistent", Detail: err.Error()}
				hardFailed = orReason(hardFailed, "deployment_identity:inconsistent")
			} else if shown.SelectedSource == deploymentidentity.SourceOverlay {
				// The bootstrap's signed-tree verification is the source of the
				// selected payload facts. Readiness still requires the process to
				// identify itself as the expected signed build on every request.
				if shown.ExpectedProcessBuildID == "" || shown.ProcessBuildID != shown.ExpectedProcessBuildID {
					checks["deployment_identity"] = CheckOutcome{OK: false, Code: "inconsistent", Detail: deploymentidentity.ErrProcessBuildMismatch.Error()}
					hardFailed = orReason(hardFailed, "deployment_identity:inconsistent")
				} else {
					checks["deployment_identity"] = CheckOutcome{OK: true, Code: "consistent"}
				}
			} else {
				checks["deployment_identity"] = CheckOutcome{OK: true, Code: "consistent"}
			}
		}
	}

	// 11. qBittorrent (degraded-only; never a hard failure).
	if c.deps.QB == nil {
		checks["qbittorrent"] = CheckOutcome{OK: true, Code: "not_configured"}
	} else {
		healthy, code := c.deps.QB.Healthy(ctx)
		checks["qbittorrent"] = CheckOutcome{OK: healthy, Code: code}
	}

	ready := hardFailed == ""
	reason := "ready"
	if !ready {
		reason = "not_ready:" + hardFailed
	} else if checks["qbittorrent"].Code != "ok" {
		reason = "degraded:qb_" + checks["qbittorrent"].Code
	}

	return Report{
		Ready: ready, Reason: reason, Checks: checks, CheckedAt: time.Now().UTC(),
		DeploymentIdentity: providedIdentity,
	}
}

func resumeGateCode(status maintenance.Status, passed bool) string {
	if passed {
		return "passed"
	}
	return "not_passed"
}

func orReason(current, next string) string {
	if current == "" {
		return next
	}
	return current + "," + next
}
