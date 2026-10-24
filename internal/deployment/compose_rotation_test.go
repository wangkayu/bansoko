package deployment

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve compose test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func TestComposeServicesHaveBoundedLogRotation(t *testing.T) {
	root := repositoryRoot(t)

	files := []string{
		"compose.yaml",
		filepath.Join("deployment", "maintainer", "docker-compose.dev.yml"),
		filepath.Join("deployment", "maintainer", "docker-compose.production.yml"),
	}
	for _, name := range files {
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			path := filepath.Join(root, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read compose file %s: %v", name, err)
			}

			var compose struct {
				Services map[string]struct {
					Image   string `yaml:"image"`
					Build   any    `yaml:"build"`
					Logging struct {
						Driver  string            `yaml:"driver"`
						Options map[string]string `yaml:"options"`
					} `yaml:"logging"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(data, &compose); err != nil {
				t.Fatalf("parse compose file %s: %v", name, err)
			}
			if len(compose.Services) == 0 {
				t.Fatalf("compose file %s has no services", name)
			}
			for _, service := range []string{"postgres", "app"} {
				if _, exists := compose.Services[service]; !exists {
					t.Errorf("compose file %s has no service %s", name, service)
				}
			}
			if _, exists := compose.Services["migrate"]; exists {
				t.Errorf("compose file %s still has a migrate service; the app bootstrap migrates before serving", name)
			}
			for service, config := range compose.Services {
				if config.Image == "" && config.Build == nil {
					t.Errorf("compose file %s service %s has neither image nor build", name, service)
				}
				if config.Logging.Driver != "local" || config.Logging.Options["max-size"] != "10m" || config.Logging.Options["max-file"] != "3" {
					t.Errorf("compose file %s service %s lacks bounded log rotation (want driver=local, max-size=10m, max-file=3)", name, service)
				}
			}
		})
	}
}

func TestQuickstartStoresUpdatesUnderConfig(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "compose.yaml"))
	if err != nil {
		t.Fatalf("read quickstart compose: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		t.Fatalf("parse quickstart compose: %v", err)
	}
	app, exists := compose.Services["app"]
	if !exists {
		t.Fatal("quickstart compose has no app service")
	}
	if got := app.Environment["BANSOKO_UPDATES_ROOT"]; got != "/config/updates" {
		t.Errorf("quickstart updates root = %q, want /config/updates", got)
	}
	if _, exists := app.Environment["BANSOKO_UPDATE_MODE"]; exists {
		t.Error("quickstart overrides the image's embedded update default")
	}
	if _, exists := compose.Volumes["updates"]; exists {
		t.Error("quickstart still declares a separate updates volume")
	}
	hasConfig := false
	for _, mount := range app.Volumes {
		if mount == "./config:/config" {
			hasConfig = true
		}
		if mount == "updates:/var/lib/bansoko/updates" {
			t.Errorf("quickstart still mounts a separate updates volume: %s", mount)
		}
	}
	if !hasConfig {
		t.Error("quickstart does not mount ./config at /config")
	}
}
