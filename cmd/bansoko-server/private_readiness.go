package main

import (
	"context"
	"os"
	"strings"

	accessapplication "bansoko/internal/accesscontrol/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	bootstrapcmd "bansoko/internal/runtime/bootstrap"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/runtime/readiness"
)

func runPrivateReadiness() int {
	return bootstrapcmd.RunPrivateReadiness(func(ctx context.Context) readiness.Report {
		cfg, err := config.FromFlags(nil)
		if err != nil || len(strings.TrimSpace(cfg.SessionSecret)) < 32 {
			return readiness.Report{Reason: "probe_config_invalid"}
		}
		db, err := database.Open(ctx, database.Config{DSN: cfg.DatabaseURL})
		if err != nil {
			return readiness.Report{Reason: "probe_database_unavailable"}
		}
		defer db.Close()
		if err := database.AssertRuntimeRoleSeparated(ctx, db); err != nil {
			return readiness.Report{Reason: "probe_runtime_role_unproven"}
		}
		store, err := configurationapplication.NewDatabaseSecretStore(db, []byte(cfg.SecretEncryptionKey), configurationpersistence.NewRepository(db))
		if err != nil {
			return readiness.Report{Reason: "probe_encryption_unavailable"}
		}
		root := deploymentidentity.UpdatesRoot(os.Getenv)
		return readiness.New(readiness.Dependencies{
			DB:                           db,
			Migrations:                   migrations.Runner{DB: db, Dir: cfg.SchemaDir},
			Build:                        buildinfo.Current(),
			Bootstrap:                    accessapplication.NewServiceWithSessionSecret(db, []byte(cfg.SessionSecret)),
			Secrets:                      secretAvailable{store: store},
			WorkersIntentionallyDisabled: true,
			Deployment:                   privateDeploymentIdentity{deploymentIdentityProvider{updatesRoot: root, requireSource: true}},
		}).Report(ctx)
	})
}

type privateDeploymentIdentity struct{ deploymentIdentityProvider }

func (p privateDeploymentIdentity) CurrentPointer() deploymentidentity.PointerState {
	pointer := p.deploymentIdentityProvider.CurrentPointer()
	if p.Identity().SelectedSource == deploymentidentity.SourceBase && pointer.Presented &&
		bootstrapcmd.VerifyPrivateBasePointer(p.updatesRoot, buildinfo.Current(), pointer) == nil {
		return deploymentidentity.PointerState{}
	}
	return pointer
}

func (p privateDeploymentIdentity) Validate() error {
	if p.Identity().SelectedSource != deploymentidentity.SourceBase {
		return p.deploymentIdentityProvider.Validate()
	}
	identity := p.Identity()
	identity.ProcessBuildID = buildinfo.Current().BuildID
	pointer := p.CurrentPointer()
	return identity.ValidateForRuntime(&pointer, true)
}
