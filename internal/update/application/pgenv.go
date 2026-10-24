package application

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// postgresEnv builds the child-process environment for whitelisted pg_dump /
// pg_restore invocations. Credentials travel only via PG* variables (never
// argv, so `ps` cannot leak the password), and the process environment's own
// PG* keys are stripped first so a stale shell value cannot override the DSN.
func postgresEnv(databaseURL string) ([]string, error) {
	parsed, err := url.Parse(strings.TrimSpace(databaseURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New(
			"embedded update requires a URL-form postgres DSN so connection facts can reach the whitelisted backup tooling safely")
	}

	facts := map[string]string{
		"PGHOST": parsed.Hostname(),
		"PGPORT": parsed.Port(),
	}
	if user := parsed.User; user != nil {
		facts["PGUSER"] = user.Username()
		if password, ok := user.Password(); ok {
			facts["PGPASSWORD"] = password
		}
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database != "" && database != "*" {
		facts["PGDATABASE"] = database
	}
	for key, values := range parsed.Query() {
		switch key {
		case "sslmode":
			if len(values) > 0 && values[0] != "" {
				facts["PGSSLMODE"] = values[0]
			}
		}
	}
	if facts["PGPORT"] == "" {
		facts["PGPORT"] = "5432"
	}

	env := make([]string, 0, len(os.Environ())+len(facts))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, reserved := facts[key]; reserved {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range facts {
		env = append(env, key+"="+value)
	}
	return env, nil
}

// PostgresChildEnv exposes the whitelisted connection-fact environment
// builder to the bootstrap consumer so both the updater and PID1 feed the PG17
// client tools from exactly one implementation (never argv-carried secrets).
func PostgresChildEnv(databaseURL string) ([]string, error) {
	return postgresEnv(databaseURL)
}

// PostgresDatabaseName returns the database component of a URL-form postgres
// DSN. The bootstrap consumer needs it as the explicit pg_restore --dbname
// target when restoring a verified backup into the main database.
func PostgresDatabaseName(databaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(databaseURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", errors.New("embedded update requires a URL-form postgres DSN")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || database == "*" {
		return "", errors.New("postgres DSN does not carry an explicit database name")
	}
	return database, nil
}
