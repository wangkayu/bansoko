// Package bootsecrets owns the deployment secrets the container generates for
// itself on first start, and the database connection the app serves with.
//
// The user-facing input is a single BANSOKO_DATABASE_URL: the connection of the
// account that owns the schema and can create roles. From it and the stored
// secrets the bootstrap derives the runtime login connection (fixed role
// RuntimeRoleName, generated password) that the serving process uses.
//
// The secrets file lives in the deployment config directory (BANSOKO_CONFIG_DIR,
// default /config) as FileName, mode 0600, replaced atomically. An explicitly
// set BANSOKO_SESSION_SECRET / BANSOKO_SECRET_ENCRYPTION_KEY always wins over
// the stored value and is never written to the file. A file that exists but
// cannot be read or parsed fails closed: regenerating the encryption key would
// orphan every stored credential.
package bootsecrets

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"bansoko/internal/runtime/bootprepare"
)

const (
	// FileName is the secrets file inside the config directory.
	FileName = "secrets.json"
	// FileVersion is the only document version this build reads and writes.
	FileVersion = 1

	EnvConfigDir           = "BANSOKO_CONFIG_DIR"
	EnvDatabaseURL         = "BANSOKO_DATABASE_URL"
	EnvSessionSecret       = "BANSOKO_SESSION_SECRET"
	EnvSecretEncryptionKey = "BANSOKO_SECRET_ENCRYPTION_KEY"
	// EnvRuntimeDatabaseURL is internal: the bootstrap sets it for the server
	// (serving pool) and the migrate child (runtime role provisioning). Users
	// never set it; the bootstrap always overwrites it.
	EnvRuntimeDatabaseURL = "BANSOKO_RUNTIME_DATABASE_URL"

	// RuntimeRoleName is the fixed runtime login role the app serves with.
	RuntimeRoleName = "bansoko_app"
	// runtimeGroupRole mirrors database.RuntimeGroupRole without importing the
	// database driver into this package.
	runtimeGroupRole = "bansoko_runtime"

	// minSecretLength matches the strictest consumer: the production session
	// secret and the deployment encryption key both require >= 32 characters.
	minSecretLength = 32
	// generatedBytes of crypto/rand become 2*generatedBytes hex characters.
	generatedBytes = 32

	fieldSessionSecret   = "session_secret"
	fieldEncryptionKey   = "secret_encryption_key"
	fieldRuntimePassword = "database_runtime_password"
)

// ErrSecretsFileUnusable marks a present secrets file that cannot be trusted.
var ErrSecretsFileUnusable = errors.New("secrets file is unusable")

// Values are the effective secrets for this process.
type Values struct {
	SessionSecret       string
	SecretEncryptionKey string
	// DatabaseRuntimePassword is the runtime login role password. It has no
	// environment override: only the bootstrap ever needs it.
	DatabaseRuntimePassword string
}

// Result reports what Resolve did.
type Result struct {
	Values Values
	// Generated lists the file fields created and persisted by this call
	// (names only, never values).
	Generated []string
	Path      string
}

type document struct {
	Version                 int    `json:"version"`
	SessionSecret           string `json:"session_secret,omitempty"`
	SecretEncryptionKey     string `json:"secret_encryption_key,omitempty"`
	DatabaseRuntimePassword string `json:"database_runtime_password,omitempty"`
}

// ConfigDir returns the deployment config directory.
func ConfigDir(getenv func(string) string) string {
	if dir := strings.TrimSpace(getenv(EnvConfigDir)); dir != "" {
		return dir
	}
	return bootprepare.DefaultConfigDir
}

// FilePath returns the secrets file path inside configDir.
func FilePath(configDir string) string { return filepath.Join(configDir, FileName) }

// Resolve loads the stored secrets, generates any that are missing (unless the
// environment overrides them), persists newly generated values atomically and
// returns the effective values.
func Resolve(configDir string, getenv func(string) string, random io.Reader) (Result, error) {
	if random == nil {
		random = rand.Reader
	}
	if err := requireConfigDir(configDir); err != nil {
		return Result{}, err
	}
	path := FilePath(configDir)
	doc, _, err := readDocument(path)
	if err != nil {
		return Result{}, err
	}
	result := Result{Path: path}
	generate := func(field string, stored *string) (string, error) {
		if *stored != "" {
			return *stored, nil
		}
		value, genErr := randomSecret(random)
		if genErr != nil {
			return "", genErr
		}
		*stored = value
		result.Generated = append(result.Generated, field)
		return value, nil
	}

	if override := envOverride(getenv, EnvSessionSecret); override != "" {
		result.Values.SessionSecret = override
	} else if result.Values.SessionSecret, err = generate(fieldSessionSecret, &doc.SessionSecret); err != nil {
		return Result{}, err
	}
	if override := envOverride(getenv, EnvSecretEncryptionKey); override != "" {
		result.Values.SecretEncryptionKey = override
	} else if result.Values.SecretEncryptionKey, err = generate(fieldEncryptionKey, &doc.SecretEncryptionKey); err != nil {
		return Result{}, err
	}
	if result.Values.DatabaseRuntimePassword, err = generate(fieldRuntimePassword, &doc.DatabaseRuntimePassword); err != nil {
		return Result{}, err
	}
	if len(result.Generated) > 0 {
		doc.Version = FileVersion
		if err := writeDocument(path, doc); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

// Load returns the effective secrets without generating anything: an
// environment override wins, otherwise the stored value, otherwise empty.
// Operator tools (recovery, key rotation) use it; a missing file is not an
// error, an unusable one is.
func Load(configDir string, getenv func(string) string) (Values, error) {
	doc, _, err := readDocument(FilePath(configDir))
	if err != nil {
		return Values{}, err
	}
	values := Values{
		SessionSecret:           doc.SessionSecret,
		SecretEncryptionKey:     doc.SecretEncryptionKey,
		DatabaseRuntimePassword: doc.DatabaseRuntimePassword,
	}
	if override := envOverride(getenv, EnvSessionSecret); override != "" {
		values.SessionSecret = override
	}
	if override := envOverride(getenv, EnvSecretEncryptionKey); override != "" {
		values.SecretEncryptionKey = override
	}
	return values, nil
}

// ReplaceEncryptionKey stores newKey as the encryption key in an existing
// secrets file. Key rotation calls it after the database was re-encrypted.
func ReplaceEncryptionKey(configDir, newKey string) error {
	if err := validSecret(newKey); err != nil {
		return fmt.Errorf("new encryption key: %w", err)
	}
	path := FilePath(configDir)
	doc, found, err := readDocument(path)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s does not exist", path)
	}
	doc.Version = FileVersion
	doc.SecretEncryptionKey = newKey
	return writeDocument(path, doc)
}

// ReplaceSessionSecret stores newSecret as the session secret in an existing
// secrets file. Session-secret rotation calls it after the stored credential
// hashes were re-keyed.
func ReplaceSessionSecret(configDir, newSecret string) error {
	if err := validSecret(newSecret); err != nil {
		return fmt.Errorf("new session secret: %w", err)
	}
	path := FilePath(configDir)
	doc, found, err := readDocument(path)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s does not exist", path)
	}
	doc.Version = FileVersion
	doc.SessionSecret = newSecret
	return writeDocument(path, doc)
}

// RuntimeDatabaseURL derives the runtime login connection from the owner
// connection: same scheme, host, port, database and parameters, with the
// runtime role and password as credentials. Errors never echo the DSN.
func RuntimeDatabaseURL(ownerDSN, password string) (string, error) {
	ownerDSN = strings.TrimSpace(ownerDSN)
	if ownerDSN == "" {
		return "", fmt.Errorf("%s is required: the connection of the database account that owns the schema", EnvDatabaseURL)
	}
	parsed, err := url.Parse(ownerDSN)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Opaque != "" {
		return "", fmt.Errorf("%s must be a postgres://user:password@host:port/database URL", EnvDatabaseURL)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		return "", fmt.Errorf("%s must name the database (postgres://user:password@host:port/database)", EnvDatabaseURL)
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return "", fmt.Errorf("%s must name the database account (postgres://user:password@host:port/database)", EnvDatabaseURL)
	}
	switch parsed.User.Username() {
	case RuntimeRoleName, runtimeGroupRole:
		return "", fmt.Errorf("%s must use the schema owner account, not the reserved role %s", EnvDatabaseURL, parsed.User.Username())
	}
	if len(password) < 16 {
		return "", errors.New("runtime role password is too short")
	}
	derived := *parsed
	derived.User = url.UserPassword(RuntimeRoleName, password)
	return derived.String(), nil
}

// RuntimeCredentials extracts the runtime login role and password from a
// derived runtime connection URL.
func RuntimeCredentials(runtimeDSN string) (role, password string, err error) {
	parsed, parseErr := url.Parse(strings.TrimSpace(runtimeDSN))
	if parseErr != nil || parsed.User == nil {
		return "", "", fmt.Errorf("%s is not a postgres URL with credentials", EnvRuntimeDatabaseURL)
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.User.Username() == "" || password == "" {
		return "", "", fmt.Errorf("%s carries no runtime role credentials", EnvRuntimeDatabaseURL)
	}
	return parsed.User.Username(), password, nil
}

func envOverride(getenv func(string) string, key string) string {
	if getenv == nil {
		return ""
	}
	if value := getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return ""
}

func requireConfigDir(configDir string) error {
	if strings.TrimSpace(configDir) == "" {
		return errors.New("config directory is not set")
	}
	info, err := os.Stat(configDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("config directory %s does not exist: mount a persistent directory there (it keeps the generated keys)", configDir)
	case err != nil:
		return fmt.Errorf("config directory %s is not accessible: %w", configDir, err)
	case !info.IsDir():
		return fmt.Errorf("config path %s is not a directory", configDir)
	}
	return nil
}

// readDocument reads and strictly validates the secrets file. found=false with
// a nil error means the file does not exist yet.
func readDocument(path string) (document, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return document{}, false, nil
	}
	if err != nil {
		return document{}, false, fmt.Errorf("%w: read %s: %v (fix its permissions; it is never regenerated)", ErrSecretsFileUnusable, path, err)
	}
	unusable := func(reason string) error {
		return fmt.Errorf("%w: %s %s (restore it from your copy; it is never regenerated because that would make stored credentials unreadable)", ErrSecretsFileUnusable, path, reason)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return document{}, false, unusable("is not valid JSON")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return document{}, false, unusable("has trailing content")
	}
	if doc.Version != FileVersion {
		return document{}, false, unusable(fmt.Sprintf("has unsupported version %d", doc.Version))
	}
	for field, value := range map[string]string{
		fieldSessionSecret:   doc.SessionSecret,
		fieldEncryptionKey:   doc.SecretEncryptionKey,
		fieldRuntimePassword: doc.DatabaseRuntimePassword,
	} {
		if value == "" {
			continue
		}
		if err := validSecret(value); err != nil {
			return document{}, false, unusable(fmt.Sprintf("has an invalid %s (%v)", field, err))
		}
	}
	return doc, true, nil
}

func validSecret(value string) error {
	if len(value) < minSecretLength {
		return fmt.Errorf("shorter than %d characters", minSecretLength)
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f {
			return errors.New("contains whitespace or control characters")
		}
	}
	return nil
}

func randomSecret(random io.Reader) (string, error) {
	buf := make([]byte, generatedBytes)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// writeDocument replaces the file atomically: temp file in the same directory,
// 0600, fsync, rename, directory fsync.
func writeDocument(path string, doc document) (err error) {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w (the config directory must be writable by the app user)", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	// A root tool (e.g. key rotation in a one-off container) must not take the
	// file away from the account the app runs as.
	if previous, statErr := os.Stat(path); statErr == nil && os.Geteuid() == 0 {
		if stat, ok := previous.Sys().(*syscall.Stat_t); ok {
			if err = tmp.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				_ = tmp.Close()
				return fmt.Errorf("write %s: keep owner: %w", path, err)
			}
		}
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	dirHandle, openErr := os.Open(dir)
	if openErr != nil {
		return fmt.Errorf("sync %s: %w", dir, openErr)
	}
	defer dirHandle.Close()
	if syncErr := dirHandle.Sync(); syncErr != nil {
		return fmt.Errorf("sync %s: %w", dir, syncErr)
	}
	return nil
}
