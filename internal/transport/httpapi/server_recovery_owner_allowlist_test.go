package httpapi

import (
	"testing"

	"bansoko/internal/transport/httpapi/generated"
	"bansoko/internal/workitems"
)

// The manual-retry HTTP surface and the store's owner admission are two halves
// of one closed set. The generated enum comes from OpenAPI, the allowlist from
// workitems; a member on one side only is either a route that always answers
// OWNER_TYPE_INVALID or a retryable owner no caller can reach.
//
// The route table here mirrors server_recovery.go. It is a mirror, not the
// authority: the owner types it names are the same ones the handlers pass, and a
// route added there without a row here fails the second loop.
func TestManualRetryRoutesMatchTheOwnerAllowlist(t *testing.T) {
	routes := map[string]string{
		string(generated.ManualRetryResultOwnerTypeRssSourceRuntime): workitems.OwnerRSSSourceRuntime,
		string(generated.ManualRetryResultOwnerTypeDownloadTask):     workitems.OwnerDownloadTask,
		string(generated.ManualRetryResultOwnerTypeArtifact):         workitems.OwnerArtifact,
	}

	admitted := make(map[string]bool)
	for _, ownerType := range workitems.ManualRetryOwnerTypes() {
		admitted[ownerType] = true
	}

	for enum, ownerType := range routes {
		if enum != ownerType {
			t.Errorf("route for %q passes owner type %q", enum, ownerType)
		}
		if !admitted[ownerType] {
			t.Errorf("route %q names owner type %q which manual retry does not admit", enum, ownerType)
		}
	}
	for ownerType := range admitted {
		found := false
		for _, routeOwner := range routes {
			if routeOwner == ownerType {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manual retry admits %q but no route reaches it", ownerType)
		}
	}

	// The enum is the published contract: every admitted type must be expressible
	// in the response, or a successful retry cannot describe its own owner.
	enumValues := map[string]bool{}
	for _, value := range []generated.ManualRetryResultOwnerType{
		generated.ManualRetryResultOwnerTypeRssSourceRuntime,
		generated.ManualRetryResultOwnerTypeDownloadTask,
		generated.ManualRetryResultOwnerTypeArtifact,
	} {
		enumValues[string(value)] = true
	}
	for ownerType := range admitted {
		if !enumValues[ownerType] {
			t.Errorf("owner type %q is admitted but is not a ManualRetryResult.owner_type enum member", ownerType)
		}
	}
	for value := range enumValues {
		if !admitted[value] {
			t.Errorf("ManualRetryResult.owner_type publishes %q which manual retry does not admit", value)
		}
	}
}
