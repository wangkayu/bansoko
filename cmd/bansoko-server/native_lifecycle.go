package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bansoko/frontend"
	"bansoko/internal/mediaprobe"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/configdir"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/migrations"
	"bansoko/internal/update/adapter/pgexec"
)

// native_lifecycle.go implements the native single-binary operator commands
// `version` and `doctor`. They are host-native: they never depend on
// container paths, and doctor is a read-only preflight (it never mutates the
// database or the update state).

// runVersion reports the binary identity used by native operators to verify a
// download or an installed build before starting it.
func runVersion(stdout io.Writer) int {
	info := buildinfo.Current()
	fmt.Fprintf(stdout, "bansoko %s (commit %s)\n", info.Version, info.Commit)
	fmt.Fprintf(stdout, "target schema version: %s\n", migrations.TargetVersion())
	fmt.Fprintf(stdout, "embedded frontend: %t\n", frontend.Embedded())
	fmt.Fprintf(stdout, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return 0
}

type doctorVerdict string

const (
	doctorOK   doctorVerdict = "ok"
	doctorWarn doctorVerdict = "warn"
	doctorFail doctorVerdict = "fail"
)

type doctorResult struct {
	name    string
	verdict doctorVerdict
	detail  string
}

// runDoctor is the read-only native preflight: it reports whether this host
// can run a native deployment (config root, PostgreSQL 17 owner connection,
// client tools) without changing any state. Exit code is 1 when any check
// fails; warnings never fail the command.
func runDoctor(args []string, stdout io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	results := []doctorResult{
		doctorConfigDir(args),
		doctorConfigDirEnv(),
		doctorPostgres(ctx),
		doctorPostgresTools(runtime.GOOS, pgexec.DefaultCandidatePaths),
		doctorMediaInfo(ctx, mediaprobe.SelfCheck),
	}
	code := 0
	for _, result := range results {
		fmt.Fprintf(stdout, "check %s: %s — %s\n", result.name, result.verdict, result.detail)
		if result.verdict == doctorFail {
			code = 1
		}
	}
	return code
}

// doctorConfigDir proves the resolved configuration root is usable: an
// existing directory must be writable; a missing one must sit under a
// writable parent so `start` can create it. The check itself writes nothing.
func doctorConfigDir(args []string) doctorResult {
	dir := configdir.Resolve(configdir.FlagValue(args), os.Getenv)
	info, err := os.Stat(dir)
	switch {
	case err == nil && info.IsDir():
		if configdir.WritableDir(dir) {
			return doctorResult{"config_dir", doctorOK, dir + " exists and is writable"}
		}
		return doctorResult{"config_dir", doctorFail, dir + " exists but is not writable by this account"}
	case err == nil:
		return doctorResult{"config_dir", doctorFail, dir + " exists but is not a directory"}
	case errors.Is(err, os.ErrNotExist):
		parent := nearestExistingDir(filepath.Dir(dir))
		if parent != "" && configdir.WritableDir(parent) {
			return doctorResult{"config_dir", doctorOK, dir + " does not exist yet; start will create it under writable " + parent}
		}
		return doctorResult{"config_dir", doctorFail, dir + " does not exist and no writable parent directory was found"}
	default:
		return doctorResult{"config_dir", doctorFail, fmt.Sprintf("%s is not inspectable: %v", dir, err)}
	}
}

// doctorConfigDirEnv surfaces the resolution actually in effect so an
// unexpected /config precedence (for example inside a container test bed) is
// visible instead of silently overriding the operator's platform default.
func doctorConfigDirEnv() doctorResult {
	if env := strings.TrimSpace(os.Getenv(configdir.EnvConfigDir)); env != "" {
		return doctorResult{"config_dir_env", doctorOK, configdir.EnvConfigDir + "=" + env}
	}
	if configdir.WritableDir(configdir.ContainerConfigDir) {
		return doctorResult{"config_dir_env", doctorWarn, configdir.ContainerConfigDir + " is present and writable and takes precedence; set " + configdir.EnvConfigDir + " to use a different native config root"}
	}
	resolved := configdir.Resolve("", os.Getenv)
	return doctorResult{"config_dir_env", doctorOK, "no " + configdir.EnvConfigDir + "; native config root resolves to " + resolved}
}

// doctorPostgres proves the owner connection: reachability, server major 17,
// and the CREATEROLE capability the unified lifecycle needs to converge the
// runtime login role. All queries are read-only.
func doctorPostgres(ctx context.Context) doctorResult {
	dsn := strings.TrimSpace(os.Getenv(bootsecrets.EnvDatabaseURL))
	if dsn == "" {
		return doctorResult{"postgres_connect", doctorFail, bootsecrets.EnvDatabaseURL + " is not set; it must be the connection of the database account that owns the schema and can create roles. 未设置数据库连接。"}
	}
	db, err := database.Open(ctx, database.Config{DSN: dsn})
	if err != nil {
		return doctorResult{"postgres_connect", doctorFail, "cannot connect with the owner DSN; verify the server is reachable and the credentials are valid"}
	}
	defer db.Close()
	var versionNum int
	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&versionNum); err != nil {
		return doctorResult{"postgres_connect", doctorFail, fmt.Sprintf("server version is not readable: %v", err)}
	}
	if major := versionNum / 10000; major != 17 {
		return doctorResult{"postgres_connect", doctorFail, fmt.Sprintf("PostgreSQL 17 is required; the server reports major %d. 需要 PostgreSQL 17，服务器版本为 %d。", major, major)}
	}
	if err := database.AssertCanManageRoles(ctx, db); err != nil {
		return doctorResult{"postgres_connect", doctorFail, "the owner connection cannot create roles; grant CREATEROLE (or use a superuser) so the runtime login role can be converged. 数据库账号缺少 CREATEROLE 权限。"}
	}
	return doctorResult{"postgres_connect", doctorOK, "PostgreSQL 17 reachable and the owner connection can manage roles"}
}

// doctorPostgresTools resolves pg_dump/pg_restore the same way the backup and
// update lanes do. Missing tools never block startup, so the hint here is the
// actionable fix instead of a runtime surprise during apply or backup.
func doctorPostgresTools(goos string, candidates []string) doctorResult {
	if err := pgexec.CheckTools(candidates); err != nil {
		mode := strings.TrimSpace(os.Getenv("BANSOKO_UPDATE_MODE"))
		if mode == "" && goos == "linux" {
			mode = config.DefaultUpdateMode
		}
		verdict := doctorWarn
		if mode == "embedded" {
			verdict = doctorFail
		}
		return doctorResult{"pg_tools", verdict, err.Error()}
	}
	return doctorResult{"pg_tools", doctorOK, "pg_dump/pg_restore resolved from the whitelisted locations"}
}

func doctorMediaInfo(ctx context.Context, check func(context.Context) (string, error)) doctorResult {
	version, err := check(ctx)
	if err != nil {
		return doctorResult{"mediainfo", doctorFail, err.Error()}
	}
	return doctorResult{"mediainfo", doctorOK, version}
}

// nearestExistingDir walks up until it finds an existing directory; empty when
// none of the ancestors exists (never for absolute paths).
func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
