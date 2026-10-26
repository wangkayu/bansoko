package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

type ConnectionKind string

const (
	ConnectionAniBT       ConnectionKind = "anibt"
	ConnectionMikan       ConnectionKind = "mikan"
	ConnectionNyaa        ConnectionKind = "nyaa"
	ConnectionBGM         ConnectionKind = "bgm"
	ConnectionQBittorrent ConnectionKind = "qbittorrent"
	ConnectionTMDB        ConnectionKind = "tmdb"
	// ConnectionHTTPFeed matches the persisted HTTP/API kind "rss_http". The
	// connections CHECK constraint has always stored that spelling, so the
	// domain constant uses it too; "http_feed" only survives in subtype table
	// and column names.
	ConnectionHTTPFeed ConnectionKind = "rss_http"
)

type ConnectionState string

const (
	ConnectionConfigured ConnectionState = "configured"
	ConnectionDegraded   ConnectionState = "degraded"
	ConnectionDisabled   ConnectionState = "disabled"
)

type SecretAction string

const (
	SecretKeep    SecretAction = "keep"
	SecretReplace SecretAction = "replace"
	SecretClear   SecretAction = "clear"
)

type SecretMutation struct {
	Action SecretAction
	Value  string
}

type HTTPFeedAuthMode string

const (
	HTTPFeedAuthNone   HTTPFeedAuthMode = "none"
	HTTPFeedAuthBasic  HTTPFeedAuthMode = "basic"
	HTTPFeedAuthBearer HTTPFeedAuthMode = "bearer"
	HTTPFeedAuthCookie HTTPFeedAuthMode = "cookie"
)

// HTTPFeedConnectionConfig is the non-secret authentication contract for an
// HTTP RSS connection. The associated SecretRef is interpreted only according
// to AuthMode; its contents never select a mode implicitly.
type HTTPFeedConnectionConfig struct {
	AuthMode HTTPFeedAuthMode `json:"auth_mode"`
	Username string           `json:"username"`
}

func (c HTTPFeedConnectionConfig) Normalize() (HTTPFeedConnectionConfig, error) {
	result := HTTPFeedConnectionConfig{
		AuthMode: HTTPFeedAuthMode(strings.ToLower(strings.TrimSpace(string(c.AuthMode)))),
		Username: strings.TrimSpace(c.Username),
	}
	if result.AuthMode == "" {
		result.AuthMode = HTTPFeedAuthNone
	}
	if len(result.Username) > 256 {
		return HTTPFeedConnectionConfig{}, errors.New("HTTP feed username must be at most 256 characters")
	}
	switch result.AuthMode {
	case HTTPFeedAuthNone:
		if result.Username != "" {
			return HTTPFeedConnectionConfig{}, errors.New("HTTP feed username is only valid for basic authentication")
		}
	case HTTPFeedAuthBasic:
		if result.Username == "" {
			return HTTPFeedConnectionConfig{}, errors.New("HTTP feed basic authentication requires a username")
		}
		if strings.Contains(result.Username, ":") {
			return HTTPFeedConnectionConfig{}, errors.New("HTTP feed basic username must not contain a colon")
		}
	case HTTPFeedAuthBearer, HTTPFeedAuthCookie:
		if result.Username != "" {
			return HTTPFeedConnectionConfig{}, errors.New("HTTP feed username is only valid for basic authentication")
		}
	default:
		return HTTPFeedConnectionConfig{}, fmt.Errorf("unsupported HTTP feed authentication mode %q", result.AuthMode)
	}
	return result, nil
}

func (m SecretMutation) Validate() error {
	switch m.Action {
	case SecretKeep:
		if m.Value != "" {
			return errors.New("secret keep cannot include a value")
		}
	case SecretReplace:
		if strings.TrimSpace(m.Value) == "" {
			return errors.New("secret replace requires a value")
		}
	case SecretClear:
		if m.Value != "" {
			return errors.New("secret clear cannot include a value")
		}
	default:
		return fmt.Errorf("unsupported secret action %q", m.Action)
	}
	return nil
}

type ConnectionInput struct {
	ID                string
	Kind              ConnectionKind
	Name              string
	Endpoint          string
	Enabled           bool
	HTTPFeedConfig    *HTTPFeedConnectionConfig
	QBittorrentConfig *QBittorrentConnectionConfig
	Secret            SecretMutation
	NetworkRouteID    string
	// ExpectedVersion is the optimistic-lock version the caller observed. When
	// the connection already exists and ExpectedVersion > 0, UpsertConnection
	// rejects a mismatch with a VersionConflict. A zero value skips the check
	// (create-or-update semantics for internal callers).
	ExpectedVersion int64
}

type Connection struct {
	ID                string
	Kind              ConnectionKind
	Name              string
	Endpoint          string
	HTTPFeedConfig    *HTTPFeedConnectionConfig
	QBittorrentConfig *QBittorrentConnectionConfig
	SecretRef         string
	NetworkRouteID    string
	State             ConnectionState
	HealthVersion     int64
	Version           int64
	UpdatedAt         time.Time
}

type NetworkRouteKind string

const (
	NetworkRouteDirect    NetworkRouteKind = "direct"
	NetworkRouteHTTPProxy NetworkRouteKind = "http_proxy"
	NetworkRouteSOCKS5    NetworkRouteKind = "socks5"
)

type NetworkRouteInput struct {
	ID       string
	Name     string
	Kind     NetworkRouteKind
	Endpoint string
	Secret   SecretMutation
}

type NetworkRoute struct {
	ID        string
	Name      string
	Kind      NetworkRouteKind
	Endpoint  string
	SecretRef string
	State     ConnectionState
	Version   int64
	UpdatedAt time.Time
}

func (input NetworkRouteInput) Normalize() (NetworkRouteInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.Name == "" || len(input.Name) > 256 {
		return NetworkRouteInput{}, errors.New("network route name is required and must be at most 256 characters")
	}
	switch input.Kind {
	case NetworkRouteDirect:
		if input.Endpoint != "" {
			return NetworkRouteInput{}, errors.New("direct network route must not include an endpoint")
		}
	case NetworkRouteHTTPProxy:
		if err := validateNetworkRouteEndpoint(input.Endpoint, "http", "https"); err != nil {
			return NetworkRouteInput{}, err
		}
	case NetworkRouteSOCKS5:
		if err := validateNetworkRouteEndpoint(input.Endpoint, "socks5", "socks5h"); err != nil {
			return NetworkRouteInput{}, err
		}
	default:
		return NetworkRouteInput{}, fmt.Errorf("unsupported network route kind %q", input.Kind)
	}
	if input.Secret.Action == "" {
		input.Secret.Action = SecretKeep
	}
	if err := input.Secret.Validate(); err != nil {
		return NetworkRouteInput{}, err
	}
	return input, nil
}

func validateNetworkRouteEndpoint(raw string, schemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("network route endpoint must be an absolute URL without credentials or fragments")
	}
	allowed := false
	for _, scheme := range schemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("network route endpoint must use %s", strings.Join(schemes, " or "))
	}
	return nil
}

func SelectSingleEnabledConnection(connections []Connection, kind ConnectionKind) (Connection, error) {
	var selected *Connection
	for index := range connections {
		connection := &connections[index]
		if connection.Kind != kind || connection.State == ConnectionDisabled {
			continue
		}
		if selected != nil {
			return Connection{}, fmt.Errorf("multiple enabled %s connections require an explicit connection selection", kind)
		}
		selected = connection
	}
	if selected == nil {
		return Connection{}, fmt.Errorf("enabled %s %w", kind, ErrNoEnabledConnection)
	}
	return *selected, nil
}

// ErrNoEnabledConnection is the sentinel wrapped by
// SelectSingleEnabledConnection when no enabled connection row exists for the
// kind. Adapters that support a code-default fallback (BGM/TMDB and, after the
// connection-model alignment, Mikan/AniBT) check errors.Is(err,
// ErrNoEnabledConnection) to decide whether to fall back. A "multiple enabled
// connections" error is a real configuration conflict and must never trigger a
// fallback, so it is never wrapped with this sentinel.
var ErrNoEnabledConnection = errors.New("enabled connection is not configured")

// DefaultEndpointForKind is the official provider base URL used when no
// connection row exists for the kind. It mirrors the code-default behavior
// BGM/TMDB already use: a missing connection is not an error, the public
// endpoint and a direct route apply. SeedDefaultConnections persists these
// same values as real rows on a fresh instance so the settings UI can show
// and edit them, but adapters never hard-fail when the row is absent.
func DefaultEndpointForKind(kind ConnectionKind) string {
	switch kind {
	case ConnectionMikan:
		return "https://mikanani.me"
	case ConnectionAniBT:
		return "https://anibt.net"
	case ConnectionNyaa:
		return "https://nyaa.si"
	case ConnectionBGM:
		return "https://api.bgm.tv"
	case ConnectionTMDB:
		return "https://api.themoviedb.org/3"
	default:
		return ""
	}
}

// DefaultConnectionForKind returns the synthetic connection a caller uses
// when no persisted row exists for the kind: the official endpoint with a
// direct route (empty NetworkRouteID). Adapters that support code-default
// fallback call SelectSingleEnabledConnection first and use this value when
// it reports no configured connection. A kind without a default endpoint has
// no fallback and must stay configured by the user.
func DefaultConnectionForKind(kind ConnectionKind) (Connection, bool) {
	endpoint := DefaultEndpointForKind(kind)
	if endpoint == "" {
		return Connection{}, false
	}
	return Connection{
		Kind:     kind,
		Endpoint: endpoint,
		State:    ConnectionConfigured,
	}, true
}

// DefaultConnectionSeed is the set of provider/metadata connections a fresh
// instance is seeded with. Each uses the official endpoint, a direct route
// (empty NetworkRouteID) and an enabled state. qBittorrent and rss_http are
// not seeded: they always require explicit user configuration.
type DefaultConnectionSeed struct {
	ID       string
	Kind     ConnectionKind
	Name     string
	Endpoint string
}

// DefaultConnectionSeeds lists the built-in connections SeedDefaultConnections
// ensures exist. IDs are stable so reseeding is idempotent and so the settings
// UI can recognize built-in rows.
func DefaultConnectionSeeds() []DefaultConnectionSeed {
	return []DefaultConnectionSeed{
		{ID: "builtin-mikan", Kind: ConnectionMikan, Name: "Mikan", Endpoint: "https://mikanani.me"},
		{ID: "builtin-anibt", Kind: ConnectionAniBT, Name: "AniBT", Endpoint: "https://anibt.net"},
		{ID: "builtin-nyaa", Kind: ConnectionNyaa, Name: "Nyaa", Endpoint: "https://nyaa.si"},
		{ID: "builtin-bgm", Kind: ConnectionBGM, Name: "Bangumi", Endpoint: "https://api.bgm.tv"},
		{ID: "builtin-tmdb", Kind: ConnectionTMDB, Name: "TMDB", Endpoint: "https://api.themoviedb.org/3"},
	}
}

// QBittorrentConnectionConfig is the non-secret, typed configuration owned by
// a named qBittorrent connection. The system idempotency tag is generated by
// the download owner and is intentionally absent from this type. Paths follow
// the same-path convention by default: qBittorrent and the app share one
// filesystem view. DownloaderRoot is an optional explicit mapping for mixed
// deployments (for example qBittorrent in a container at a different path);
// when empty the application-visible root falls back to SaveRoot.
type QBittorrentConnectionConfig struct {
	Username        string   `json:"username"`
	Category        string   `json:"category"`
	UserTags        []string `json:"user_tags"`
	SaveRoot        string   `json:"save_root"`
	ApplicationRoot string   `json:"downloader_root"`
}

func (c QBittorrentConnectionConfig) Normalize() (QBittorrentConnectionConfig, error) {
	result := QBittorrentConnectionConfig{
		Username:        strings.TrimSpace(c.Username),
		Category:        strings.TrimSpace(c.Category),
		SaveRoot:        strings.TrimSpace(c.SaveRoot),
		ApplicationRoot: strings.TrimSpace(c.ApplicationRoot),
	}
	if result.Username == "" {
		return QBittorrentConnectionConfig{}, errors.New("qBittorrent username is required")
	}
	if len(result.Username) > 256 || len(result.Category) > 256 || len(result.SaveRoot) > 4096 || len(result.ApplicationRoot) > 4096 {
		return QBittorrentConnectionConfig{}, errors.New("qBittorrent connection config exceeds the allowed length")
	}
	if result.SaveRoot != "" {
		normalizedSaveRoot, err := normalizeMappedPath(result.SaveRoot, "save_root")
		if err != nil {
			return QBittorrentConnectionConfig{}, err
		}
		result.SaveRoot = normalizedSaveRoot
	}
	if result.ApplicationRoot != "" {
		normalizedApplicationRoot, err := normalizeMappedPath(result.ApplicationRoot, "downloader_root")
		if err != nil {
			return QBittorrentConnectionConfig{}, err
		}
		result.ApplicationRoot = normalizedApplicationRoot
	}

	seenTags := make(map[string]struct{}, len(c.UserTags))
	for _, rawTag := range c.UserTags {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			return QBittorrentConnectionConfig{}, errors.New("qBittorrent user tags cannot be empty")
		}
		if strings.HasPrefix(strings.ToLower(tag), "bansoko-") {
			return QBittorrentConnectionConfig{}, fmt.Errorf("qBittorrent user tag %q uses the reserved system prefix", tag)
		}
		if strings.ContainsAny(tag, ",\r\n") {
			return QBittorrentConnectionConfig{}, fmt.Errorf("qBittorrent user tag %q cannot contain delimiters", tag)
		}
		if len(tag) > 256 {
			return QBittorrentConnectionConfig{}, errors.New("qBittorrent user tag exceeds 256 characters")
		}
		if _, exists := seenTags[tag]; exists {
			return QBittorrentConnectionConfig{}, fmt.Errorf("qBittorrent user tag %q is duplicated", tag)
		}
		seenTags[tag] = struct{}{}
		result.UserTags = append(result.UserTags, tag)
	}
	sort.Strings(result.UserTags)
	return result, nil
}

func normalizeMappedPath(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("qBittorrent %s is required", field)
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", fmt.Errorf("qBittorrent %s cannot contain parent traversal", field)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == "" || len(cleaned) > 4096 {
		return "", fmt.Errorf("qBittorrent %s is invalid", field)
	}
	return cleaned, nil
}

type GlobalSectionInput struct {
	SchemaVersion string
	Value         map[string]any
}

type GlobalSectionView struct {
	SectionName   string
	SchemaVersion string
	Value         map[string]any
	Version       int64
	// Saved is true when a persisted global row backs the value; false for the
	// built-in code default.
	Saved bool
}

// NamingTemplate is one entry of the naming template library. Rules is the
// complete typed naming rules value (naming-rules-v1).
type NamingTemplate struct {
	Key   string
	Name  string
	Rules json.RawMessage
}

// NamingTemplateLibraryInput replaces the whole library with expected-version
// CAS. DefaultKey must name one of Templates.
type NamingTemplateLibraryInput struct {
	DefaultKey string
	Templates  []NamingTemplate
}

// NamingTemplateLibraryView is the library with every template body resolved;
// the default template's body is the effective global naming.
type NamingTemplateLibraryView struct {
	DefaultKey string
	Templates  []NamingTemplate
	Version    int64
}

type QBittorrentServerStatusView struct {
	Version          string
	ConnectionStatus string
	DownloadSpeed    int64
	UploadSpeed      int64
	FreeSpace        int64
	TotalSize        int64
}

type QBittorrentTorrentView struct {
	AcquisitionOwner string // "subscription" | "one_shot" while its acquisition is running; empty otherwise
	Origin           string // "subscription" | "one_shot" | "unknown" | "external": who created the task (see QBManagementService.ListTorrents)
	AddedOn          int64
	Category         string
	DownloadSpeed    int64
	Downloaded       int64
	Eta              int64
	Hash             string
	Name             string
	Progress         float64
	SavePath         string
	Size             int64
	State            string
	Tags             string
	Uploaded         int64
	UploadSpeed      int64
}

type QBittorrentTorrentFileView struct {
	Name     string
	Priority int
	Progress float64
	Size     int64
}

type HealthSnapshot struct {
	ConnectionID string
	Kind         ConnectionKind
	State        ConnectionState
	Code         string
	Message      string
	CheckedAt    time.Time
	Version      int64
}

type ConnectionTestResult struct {
	CheckedAt    time.Time
	ConnectionID string
	Health       string
	Kind         ConnectionKind
	ReasonCode   *string
	ReasonDetail *string
}

type NetworkRouteTestResult struct {
	CheckedAt    time.Time
	Health       string
	Kind         NetworkRouteKind
	ReasonCode   *string
	ReasonDetail *string
	RouteID      string
}
