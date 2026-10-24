package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/database"
)

// runRotateSecretKey is `bansoko-migrate rotate-secret-key` (SEC-033): re-encrypt
// every stored secret value from the current deployment key (BANSOKO_SECRET_ENCRYPTION_KEY
// when set, else the generated key in the config directory secrets file) to
// BANSOKO_NEW_SECRET_ENCRYPTION_KEY in one transaction, against BANSOKO_DATABASE_URL (the
// schema owner connection). A stored key is replaced in the secrets file after the
// commit. It refuses while any other session is connected to
// the database, so the app must be stopped first; deployment/scripts/
// rotate-secret-key.sh does that and runs this inside the app image. Keys and
// the DSN are read from the environment only, never from argv, and never
// printed.
func runRotateSecretKey(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	// The current key is the explicit environment override when set, else the
	// generated key stored in the config directory.
	configDir := bootsecrets.ConfigDir(getenv)
	keyFromEnv := strings.TrimSpace(getenv(bootsecrets.EnvSecretEncryptionKey)) != ""
	stored, loadErr := bootsecrets.Load(configDir, getenv)
	if loadErr != nil {
		fmt.Fprintf(stderr, "rotate-secret-key: %v\n", loadErr)
		return 2
	}
	currentKey := stored.SecretEncryptionKey
	newKey := getenv("BANSOKO_NEW_SECRET_ENCRYPTION_KEY")
	dsn := strings.TrimSpace(getenv("BANSOKO_DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(stderr, "rotate-secret-key: BANSOKO_DATABASE_URL is required")
		return 2
	}
	if len(strings.TrimSpace(currentKey)) < 32 || len(strings.TrimSpace(newKey)) < 32 {
		fmt.Fprintln(stderr, "rotate-secret-key: set BANSOKO_SECRET_ENCRYPTION_KEY (current) and BANSOKO_NEW_SECRET_ENCRYPTION_KEY (new), each at least 32 characters")
		return 2
	}
	db, err := database.Open(ctx, database.Config{DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		fmt.Fprintln(stderr, "rotate-secret-key: cannot open the database")
		return 1
	}
	defer db.Close()
	rotated, err := configurationapplication.RotateDeploymentKey(ctx, db, []byte(currentKey), []byte(newKey),
		configurationapplication.RotateOptions{RequireExclusive: true})
	if err != nil {
		var decryptErr *configurationapplication.RotationDecryptError
		switch {
		case errors.As(err, &decryptErr), errors.Is(err, configurationapplication.ErrRotationOtherSessions),
			errors.Is(err, configurationapplication.ErrRotationKeyInvalid):
			fmt.Fprintf(stderr, "rotate-secret-key: %v\n", err)
		default:
			fmt.Fprintf(stderr, "rotate-secret-key: rotation failed; nothing was changed (%s)\n", safeMigrationErrorClass(err))
		}
		return 1
	}
	if keyFromEnv {
		fmt.Fprintf(stdout, "rotate-secret-key: re-encrypted %d secret values; set BANSOKO_SECRET_ENCRYPTION_KEY to the new key before starting the app\n", rotated)
		return 0
	}
	if err := bootsecrets.ReplaceEncryptionKey(configDir, newKey); err != nil {
		fmt.Fprintf(stderr, "rotate-secret-key: re-encrypted %d secret values, but storing the new key failed (%v); put the new key into %s or set BANSOKO_SECRET_ENCRYPTION_KEY before starting the app\n",
			rotated, err, bootsecrets.FilePath(configDir))
		return 1
	}
	fmt.Fprintf(stdout, "rotate-secret-key: re-encrypted %d secret values and stored the new key in %s\n", rotated, bootsecrets.FilePath(configDir))
	return 0
}
