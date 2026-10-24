package qbittorrent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// NamespaceLocation is the reconcilable non-secret anchor for one qB provider
// namespace (effect-contract 4.2). It deliberately keeps the credential-free
// endpoint, the path mapping and the trusted connection reference with its
// version, so that:
//
//   - rotating a credential does not change the digest: the naming intent belongs
//     to the namespace, and a secret ref is never part of it;
//   - repointing the same connection id at a different endpoint does change it,
//     so a lookup against the new qB can never be passed off as evidence about
//     the old call;
//   - the anchor stays auditable after compaction, because it is stored as data
//     beside the digest rather than being flattened into an irreversible hash.
//
// It never carries a password, a token or any other secret.
type NamespaceLocation struct {
	// Endpoint is scheme://host:port plus any path prefix, with credentials
	// stripped. It is identity, not a route to be dialled.
	Endpoint string `json:"endpoint"`
	// SaveRoot is the downloader-side root the client writes to.
	SaveRoot string `json:"save_root"`
	// ApplicationRoot is the local root mapped from SaveRoot for mixed
	// deployments. Empty means the same-path convention, which is itself a fact
	// worth recording rather than omitting.
	ApplicationRoot string `json:"application_root"`
	// ConnectionRef is the trusted owner reference of the selected connection.
	ConnectionRef string `json:"connection_ref"`
	// ConnectionVersion is that reference's row version, so a re-configured
	// connection is a new namespace rather than a silently reused one.
	ConnectionVersion int64 `json:"connection_version"`
}

// NewNamespaceLocation derives the anchor from the resolved runtime config. A
// malformed endpoint yields an empty Endpoint rather than a partially parsed
// one: a namespace that cannot be named must not be guessed at.
func NewNamespaceLocation(config RuntimeConfig) NamespaceLocation {
	return NamespaceLocation{
		Endpoint:          credentialFreeEndpoint(config.Endpoint),
		SaveRoot:          strings.TrimSpace(config.SaveRoot),
		ApplicationRoot:   strings.TrimSpace(config.ApplicationRoot),
		ConnectionRef:     strings.TrimSpace(config.ConnectionID),
		ConnectionVersion: config.ConnectionVersion,
	}
}

// credentialFreeEndpoint keeps the parts of a URL that identify the instance and
// drops the userinfo, which is the only place a credential can hide.
func credentialFreeEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	// A scheme and a host are both required: without them there is no instance to
	// name, and emitting a half-rendered identity would be worse than nothing.
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	identity := parsed.Scheme + "://" + parsed.Host
	path := strings.TrimSuffix(parsed.Path, "/")
	if path != "" {
		identity += path
	}
	return identity
}

// Digest is the stable fingerprint of this namespace. It is a function of the
// location alone, so two runs that resolve the same namespace agree byte for
// byte.
func (l NamespaceLocation) Digest() string {
	sum := sha256.Sum256([]byte(l.canonical()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonical renders the location deterministically. The fields are always
// present -- including empty ones -- so a missing ApplicationRoot can never be
// confused with a present one that happens to be empty.
func (l NamespaceLocation) canonical() string {
	raw, err := json.Marshal([]string{
		"qbittorrent-namespace:v1",
		l.Endpoint, l.SaveRoot, l.ApplicationRoot, l.ConnectionRef,
		strconv.FormatInt(l.ConnectionVersion, 10),
	})
	if err != nil {
		return "qbittorrent-namespace:v1"
	}
	return string(raw)
}
