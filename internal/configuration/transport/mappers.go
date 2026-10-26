package transport

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func GlobalSectionInput(value generated.GlobalSectionInput) configurationdomain.GlobalSectionInput {
	return configurationdomain.GlobalSectionInput{
		SchemaVersion: value.SchemaVersion,
		Value:         cloneJSONMap(value.Value),
	}
}

func GlobalSectionView(value configurationdomain.GlobalSectionView) generated.GlobalSectionView {
	return generated.GlobalSectionView{
		SectionName:   generated.GlobalSectionName(value.SectionName),
		SchemaVersion: value.SchemaVersion,
		Value:         cloneJSONMap(value.Value),
		Version:       value.Version,
		Saved:         value.Saved,
	}
}

func GlobalSectionViews(values []configurationdomain.GlobalSectionView) []generated.GlobalSectionView {
	result := make([]generated.GlobalSectionView, 0, len(values))
	for _, value := range values {
		result = append(result, GlobalSectionView(value))
	}
	return result
}

func QBittorrentTorrents(values []configurationdomain.QBittorrentTorrentView) []generated.QBittorrentTorrent {
	result := make([]generated.QBittorrentTorrent, 0, len(values))
	for _, value := range values {
		result = append(result, QBittorrentTorrent(value))
	}
	return result
}

func QBittorrentTorrent(value configurationdomain.QBittorrentTorrentView) generated.QBittorrentTorrent {
	addedOn := value.AddedOn
	category := value.Category
	dlspeed := value.DownloadSpeed
	downloaded := value.Downloaded
	eta := value.Eta
	savePath := value.SavePath
	tags := value.Tags
	uploaded := value.Uploaded
	upspeed := value.UploadSpeed
	var owner *generated.QBittorrentTorrentAcquisitionOwner
	if value.AcquisitionOwner != "" {
		o := generated.QBittorrentTorrentAcquisitionOwner(value.AcquisitionOwner)
		owner = &o
	}
	return generated.QBittorrentTorrent{
		AcquisitionOwner: owner,
		Origin:           generated.QBittorrentTorrentOrigin(value.Origin),
		AddedOn:          &addedOn,
		Category:         &category,
		Dlspeed:          &dlspeed,
		Downloaded:       &downloaded,
		Eta:              &eta,
		Hash:             value.Hash,
		Name:             value.Name,
		Progress:         value.Progress,
		SavePath:         &savePath,
		Size:             value.Size,
		State:            value.State,
		Tags:             &tags,
		Uploaded:         &uploaded,
		Upspeed:          &upspeed,
	}
}

func QBittorrentTorrentFiles(values []configurationdomain.QBittorrentTorrentFileView) []generated.QBittorrentTorrentFile {
	result := make([]generated.QBittorrentTorrentFile, 0, len(values))
	for _, value := range values {
		result = append(result, QBittorrentTorrentFile(value))
	}
	return result
}

func QBittorrentTorrentFile(value configurationdomain.QBittorrentTorrentFileView) generated.QBittorrentTorrentFile {
	priority := value.Priority
	return generated.QBittorrentTorrentFile{
		Name:     value.Name,
		Size:     value.Size,
		Progress: value.Progress,
		Priority: &priority,
	}
}

func QBittorrentServerStatus(value configurationdomain.QBittorrentServerStatusView) generated.QBittorrentServerStatus {
	downloadSpeed := value.DownloadSpeed
	freeSpace := value.FreeSpace
	totalSize := value.TotalSize
	uploadSpeed := value.UploadSpeed
	return generated.QBittorrentServerStatus{
		Version:          value.Version,
		ConnectionStatus: value.ConnectionStatus,
		DlSpeed:          &downloadSpeed,
		FreeSpace:        &freeSpace,
		TotalSize:        &totalSize,
		UpSpeed:          &uploadSpeed,
	}
}

func ConnectionInput(input generated.ConnectionInput, id string) (configurationdomain.ConnectionInput, error) {
	kind, err := input.Discriminator()
	if err != nil {
		return configurationdomain.ConnectionInput{}, err
	}
	result := configurationdomain.ConnectionInput{ID: id, Kind: configurationdomain.ConnectionKind(kind), Secret: configurationdomain.SecretMutation{Action: configurationdomain.SecretKeep}}
	switch kind {
	case "qbittorrent":
		v, err := input.AsQBittorrentConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint, result.QBittorrentConfig = v.Name, v.BaseUrl, &configurationdomain.QBittorrentConnectionConfig{Username: v.Username}
		result.Enabled = v.Enabled
		result.QBittorrentConfig.SaveRoot = v.DownloadRoot
		if v.DownloaderRoot != nil {
			result.QBittorrentConfig.ApplicationRoot = *v.DownloaderRoot
		}
		if v.Category != nil {
			result.QBittorrentConfig.Category = *v.Category
		}
		if v.UserTags != nil {
			result.QBittorrentConfig.UserTags = *v.UserTags
		}
		secret, err := ConnectionSecretMutation(v.Secret)
		if err != nil {
			return result, err
		}
		result.Secret = secret
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "tmdb":
		v, err := input.AsTMDBConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint = v.Name, v.BaseUrl
		result.Enabled = v.Enabled
		secret, err := ConnectionSecretMutation(v.Secret)
		if err != nil {
			return result, err
		}
		result.Secret = secret
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "anibt":
		v, err := input.AsAniBTConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint = v.Name, v.BaseUrl
		result.Enabled = v.Enabled
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "mikan":
		v, err := input.AsMikanConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint = v.Name, v.BaseUrl
		result.Enabled = v.Enabled
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "nyaa":
		v, err := input.AsNyaaConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint = v.Name, v.BaseUrl
		result.Enabled = v.Enabled
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "bgm":
		v, err := input.AsBGMConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name, result.Endpoint = v.Name, v.BaseUrl
		result.Enabled = v.Enabled
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	case "rss_http":
		v, err := input.AsRSSHTTPConnectionInput()
		if err != nil {
			return result, err
		}
		result.Name = v.Name
		result.Enabled = v.Enabled
		result.Endpoint = RssHTTPEndpointPlaceholder
		result.HTTPFeedConfig = &configurationdomain.HTTPFeedConnectionConfig{AuthMode: configurationdomain.HTTPFeedAuthNone}
		if v.RouteId != nil {
			result.NetworkRouteID = string(*v.RouteId)
		}
	default:
		return result, fmt.Errorf("unsupported connection kind %q", kind)
	}
	return result, nil
}

const RssHTTPEndpointPlaceholder = "rss-http://transport"

func ConnectionSecretMutation(secret generated.ConnectionSecret) (configurationdomain.SecretMutation, error) {
	invalid := &apperrors.BadRequest{Code: "CONNECTION_SECRET_INVALID", Message: "Connection secret must declare mode keep, replace, or remove."}
	mode, err := secret.Discriminator()
	if err != nil {
		return configurationdomain.SecretMutation{}, invalid
	}
	switch mode {
	case "keep":
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretKeep}, nil
	case "replace":
		v, err := secret.AsConnectionSecretReplace()
		if err != nil {
			return configurationdomain.SecretMutation{}, invalid
		}
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretReplace, Value: v.Value}, nil
	case "remove":
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretClear}, nil
	default:
		return configurationdomain.SecretMutation{}, invalid
	}
}

func ConnectionView(item configurationdomain.Connection) (generated.Connection, error) {
	var result generated.Connection
	switch item.Kind {
	case configurationdomain.ConnectionQBittorrent:
		v := generated.QBittorrentConnection{Id: item.ID, Kind: "qbittorrent", Name: item.Name, BaseUrl: item.Endpoint, Username: item.QBittorrentConfig.Username, Enabled: item.State != configurationdomain.ConnectionDisabled, CredentialConfigured: item.SecretRef != "", Version: item.Version}
		if item.QBittorrentConfig.SaveRoot != "" {
			v.DownloadRoot = &item.QBittorrentConfig.SaveRoot
		}
		if item.QBittorrentConfig.ApplicationRoot != "" {
			v.DownloaderRoot = &item.QBittorrentConfig.ApplicationRoot
		}
		if item.QBittorrentConfig.Category != "" {
			v.Category = &item.QBittorrentConfig.Category
		}
		if len(item.QBittorrentConfig.UserTags) > 0 {
			tags := append([]string(nil), item.QBittorrentConfig.UserTags...)
			v.UserTags = &tags
		}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromQBittorrentConnection(v)
	case configurationdomain.ConnectionTMDB:
		v := generated.TMDBConnection{Id: item.ID, Kind: "tmdb", Name: item.Name, BaseUrl: item.Endpoint, Enabled: item.State != configurationdomain.ConnectionDisabled, CredentialConfigured: item.SecretRef != "", Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromTMDBConnection(v)
	case configurationdomain.ConnectionAniBT:
		v := generated.AniBTConnection{Id: item.ID, Kind: "anibt", Name: item.Name, BaseUrl: item.Endpoint, Enabled: item.State != configurationdomain.ConnectionDisabled, Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromAniBTConnection(v)
	case configurationdomain.ConnectionMikan:
		v := generated.MikanConnection{Id: item.ID, Kind: "mikan", Name: item.Name, BaseUrl: item.Endpoint, Enabled: item.State != configurationdomain.ConnectionDisabled, Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromMikanConnection(v)
	case configurationdomain.ConnectionNyaa:
		v := generated.NyaaConnection{Id: item.ID, Kind: "nyaa", Name: item.Name, BaseUrl: item.Endpoint, Enabled: item.State != configurationdomain.ConnectionDisabled, Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromNyaaConnection(v)
	case configurationdomain.ConnectionBGM:
		v := generated.BGMConnection{Id: item.ID, Kind: "bgm", Name: item.Name, BaseUrl: item.Endpoint, Enabled: item.State != configurationdomain.ConnectionDisabled, Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromBGMConnection(v)
	case configurationdomain.ConnectionHTTPFeed:
		v := generated.RSSHTTPConnection{Id: item.ID, Kind: "rss_http", Name: item.Name, Enabled: item.State != configurationdomain.ConnectionDisabled, Version: item.Version}
		if item.NetworkRouteID != "" {
			v.RouteId = &item.NetworkRouteID
		}
		return result, result.FromRSSHTTPConnection(v)
	default:
		return result, fmt.Errorf("unsupported connection kind %q", item.Kind)
	}
}

func ConnectionsView(items []configurationdomain.Connection) ([]generated.Connection, error) {
	result := make([]generated.Connection, 0, len(items))
	for _, item := range items {
		view, err := ConnectionView(item)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func NetworkRouteInput(input generated.NetworkRouteInput, id string) (configurationdomain.NetworkRouteInput, error) {
	kind, err := input.Discriminator()
	if err != nil {
		return configurationdomain.NetworkRouteInput{}, err
	}
	result := configurationdomain.NetworkRouteInput{ID: id, Kind: configurationdomain.NetworkRouteKind(kind), Secret: configurationdomain.SecretMutation{Action: configurationdomain.SecretKeep}}
	switch kind {
	case "direct":
		v, e := input.AsDirectRouteInput()
		if e != nil {
			return result, e
		}
		result.Name = v.Name
	case "http_proxy":
		v, e := input.AsHTTPProxyRouteInput()
		if e != nil {
			return result, e
		}
		result.Name, result.Endpoint = v.Name, v.ProxyUrl
		secret, err := routeSecretMutation(v.Secret, v.Username)
		if err != nil {
			return result, err
		}
		result.Secret = secret
	case "socks5":
		v, e := input.AsSOCKS5RouteInput()
		if e != nil {
			return result, e
		}
		result.Name, result.Endpoint = v.Name, "socks5://"+v.Host+":"+fmt.Sprint(v.Port)
		secret, err := routeSecretMutation(v.Secret, v.Username)
		if err != nil {
			return result, err
		}
		result.Secret = secret
	default:
		return result, fmt.Errorf("unsupported route kind %q", kind)
	}
	return result, nil
}

func routeSecretMutation(secret *generated.ConnectionSecret, username *string) (configurationdomain.SecretMutation, error) {
	invalid := &apperrors.BadRequest{Code: "ROUTE_SECRET_INVALID", Message: "Route secret must declare mode keep, replace, or remove."}
	if secret == nil {
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretKeep}, nil
	}
	mode, err := secret.Discriminator()
	if err != nil {
		return configurationdomain.SecretMutation{}, invalid
	}
	switch mode {
	case "keep":
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretKeep}, nil
	case "remove":
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretClear}, nil
	case "replace":
		v, err := secret.AsConnectionSecretReplace()
		if err != nil {
			return configurationdomain.SecretMutation{}, invalid
		}
		if username == nil || strings.TrimSpace(*username) == "" {
			return configurationdomain.SecretMutation{}, &apperrors.BadRequest{Code: "ROUTE_SECRET_INVALID", Message: "Replacing a route credential requires a username."}
		}
		return configurationdomain.SecretMutation{Action: configurationdomain.SecretReplace, Value: strings.TrimSpace(*username) + ":" + v.Value}, nil
	default:
		return configurationdomain.SecretMutation{}, invalid
	}
}

func NetworkRouteView(item configurationdomain.NetworkRoute) (generated.NetworkRoute, error) {
	enabled := item.State != configurationdomain.ConnectionDisabled
	credentialConfigured := item.SecretRef != ""
	var result generated.NetworkRoute
	switch item.Kind {
	case configurationdomain.NetworkRouteDirect:
		return result, result.FromDirectRoute(generated.DirectRoute{
			Id: item.ID, Kind: "direct", Name: item.Name, Enabled: enabled, Version: item.Version,
		})
	case configurationdomain.NetworkRouteHTTPProxy:
		return result, result.FromHTTPProxyRoute(generated.HTTPProxyRoute{
			Id: item.ID, Kind: "http_proxy", Name: item.Name, Enabled: enabled,
			ProxyUrl: item.Endpoint, CredentialConfigured: credentialConfigured, Version: item.Version,
		})
	case configurationdomain.NetworkRouteSOCKS5:
		parsed, err := url.Parse(item.Endpoint)
		if err != nil {
			return result, fmt.Errorf("parse socks5 route endpoint: %w", err)
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || parsed.Hostname() == "" {
			return result, fmt.Errorf("socks5 route endpoint %q is not host:port", item.Endpoint)
		}
		return result, result.FromSOCKS5Route(generated.SOCKS5Route{
			Id: item.ID, Kind: "socks5", Name: item.Name, Enabled: enabled,
			Host: parsed.Hostname(), Port: port, CredentialConfigured: credentialConfigured, Version: item.Version,
		})
	default:
		return result, fmt.Errorf("unsupported route kind %q", item.Kind)
	}
}

func NetworkRoutesView(items []configurationdomain.NetworkRoute) ([]generated.NetworkRoute, error) {
	result := make([]generated.NetworkRoute, 0, len(items))
	for _, item := range items {
		view, err := NetworkRouteView(item)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func ConnectionTestResult(value configurationdomain.ConnectionTestResult) generated.ConnectionTestResult {
	return generated.ConnectionTestResult{
		CheckedAt:    value.CheckedAt,
		ConnectionId: value.ConnectionID,
		Health:       generated.ConnectionTestResultHealth(value.Health),
		Kind:         generated.ConnectionTestResultKind(value.Kind),
		ReasonCode:   value.ReasonCode,
		ReasonDetail: value.ReasonDetail,
	}
}

func NetworkRouteTestResult(value configurationdomain.NetworkRouteTestResult) generated.NetworkRouteTestResult {
	return generated.NetworkRouteTestResult{
		CheckedAt:    value.CheckedAt,
		Health:       generated.NetworkRouteTestResultHealth(value.Health),
		Kind:         generated.NetworkRouteTestResultKind(value.Kind),
		ReasonCode:   value.ReasonCode,
		ReasonDetail: value.ReasonDetail,
		RouteId:      value.RouteID,
	}
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

// NamingTemplateLibraryInput maps the wire library; each typed rules body is
// re-encoded as the stored naming-rules JSON.
func NamingTemplateLibraryInput(value generated.NamingTemplateLibraryInput) (configurationdomain.NamingTemplateLibraryInput, error) {
	result := configurationdomain.NamingTemplateLibraryInput{DefaultKey: value.DefaultKey, Templates: make([]configurationdomain.NamingTemplate, 0, len(value.Templates))}
	for _, item := range value.Templates {
		rules, err := json.Marshal(item.Rules)
		if err != nil {
			return configurationdomain.NamingTemplateLibraryInput{}, fmt.Errorf("encode naming template %q rules: %w", item.Key, err)
		}
		result.Templates = append(result.Templates, configurationdomain.NamingTemplate{Key: item.Key, Name: item.Name, Rules: rules})
	}
	return result, nil
}

// NamingTemplateLibraryView maps the resolved library to the wire view.
func NamingTemplateLibraryView(value configurationdomain.NamingTemplateLibraryView) (generated.NamingTemplateLibraryView, error) {
	result := generated.NamingTemplateLibraryView{DefaultKey: value.DefaultKey, Version: value.Version, Templates: make([]generated.NamingTemplate, 0, len(value.Templates))}
	for _, item := range value.Templates {
		var rules generated.NamingRules
		if err := json.Unmarshal(item.Rules, &rules); err != nil {
			return generated.NamingTemplateLibraryView{}, fmt.Errorf("decode naming template %q rules: %w", item.Key, err)
		}
		result.Templates = append(result.Templates, generated.NamingTemplate{Key: item.Key, Name: item.Name, Rules: rules})
	}
	return result, nil
}
