package migratecmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	accessapplication "bansoko/internal/accesscontrol/application"
	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/database"
)

// runRotateSessionSecret is `bansoko-migrate rotate-session-secret` (SEC-033):
// re-key the session and API-token hashes for a new BANSOKO_SESSION_SECRET and
// store it in the config directory secrets file. The new secret is generated
// unless BANSOKO_NEW_SESSION_SECRET supplies one.
//
// Session tokens are HMAC'd with the session secret, so the rotation ends every
// web session. API tokens are re-hashed from their encrypted raw values, which
// needs BANSOKO_SECRET_ENCRYPTION_KEY (the current one); tokens whose raw value
// cannot be recovered are dropped, because after the rotation their hash matches
// nothing. It refuses while the app is still connected to the database, so the
// rotation script stops it first. Secrets and the DSN are read from the
// environment only, never from argv, and never printed.
func runRotateSessionSecret(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	configDir := bootsecrets.ConfigDir(getenv)
	secretFromEnv := strings.TrimSpace(getenv(bootsecrets.EnvSessionSecret)) != ""
	stored, loadErr := bootsecrets.Load(configDir, getenv)
	if loadErr != nil {
		fmt.Fprintf(stderr, "rotate-session-secret: %v\n", loadErr)
		return 2
	}
	newSecret := strings.TrimSpace(getenv("BANSOKO_NEW_SESSION_SECRET"))
	dsn := strings.TrimSpace(getenv(bootsecrets.EnvDatabaseURL))
	if dsn == "" {
		fmt.Fprintln(stderr, "rotate-session-secret: BANSOKO_DATABASE_URL is required")
		return 2
	}
	if newSecret == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			fmt.Fprintf(stderr, "rotate-session-secret: cannot generate a new secret: %v\n", err)
			return 1
		}
		newSecret = hex.EncodeToString(buf)
	} else if len(newSecret) < 32 {
		fmt.Fprintln(stderr, "rotate-session-secret: BANSOKO_NEW_SESSION_SECRET must be at least 32 characters")
		return 2
	}
	if newSecret == stored.SessionSecret {
		fmt.Fprintln(stderr, "rotate-session-secret: the new secret is the current one; nothing to rotate")
		return 2
	}
	if strings.TrimSpace(stored.SecretEncryptionKey) == "" {
		fmt.Fprintln(stderr, "rotate-session-secret: no deployment key is available, so the stored API tokens cannot be re-hashed")
		return 2
	}
	db, err := database.Open(ctx, database.Config{DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		fmt.Fprintln(stderr, "rotate-session-secret: cannot open the database")
		return 1
	}
	defer db.Close()
	secrets, err := configurationapplication.NewDatabaseSecretStore(db, []byte(stored.SecretEncryptionKey), configurationpersistence.NewRepository(db))
	if err != nil {
		fmt.Fprintf(stderr, "rotate-session-secret: %v\n", err)
		return 1
	}
	result, err := accessapplication.RotateSessionSecret(ctx, db, []byte(newSecret), secrets.Resolve, true)
	if err != nil {
		switch {
		case errors.Is(err, accessapplication.ErrSessionRotationSecretInvalid),
			errors.Is(err, accessapplication.ErrSessionRotationOtherSessions):
			fmt.Fprintf(stderr, "rotate-session-secret: %v\n", err)
		default:
			fmt.Fprintf(stderr, "rotate-session-secret: rotation failed; nothing was changed (%s)\n", safeMigrationErrorClass(err))
		}
		return 1
	}
	summary := fmt.Sprintf("revoked %d web sessions and re-hashed %d API tokens", result.SessionsRevoked, result.TokensRehashed)
	if len(result.TokensDropped) > 0 {
		summary += fmt.Sprintf("; dropped %d whose raw value could not be recovered (%s)", len(result.TokensDropped), strings.Join(result.TokensDropped, ", "))
	}
	if secretFromEnv {
		fmt.Fprintf(stdout, "rotate-session-secret: %s; set BANSOKO_SESSION_SECRET to the new secret before starting the app\n", summary)
		return 0
	}
	if err := bootsecrets.ReplaceSessionSecret(configDir, newSecret); err != nil {
		fmt.Fprintf(stderr, "rotate-session-secret: %s, but storing the new secret failed (%v); put it into %s or set BANSOKO_SESSION_SECRET before starting the app\n",
			summary, err, bootsecrets.FilePath(configDir))
		return 1
	}
	fmt.Fprintf(stdout, "rotate-session-secret: %s and stored the new secret in %s\n", summary, bootsecrets.FilePath(configDir))
	return 0
}
