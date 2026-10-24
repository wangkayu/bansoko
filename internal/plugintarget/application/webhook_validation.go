package application

import "strings"

var forbiddenAuthHeaders = map[string]bool{
	"host": true, "content-length": true, "cookie": true, "connection": true,
	"proxy-authorization": true, "proxy-authenticate": true, "transfer-encoding": true,
	"upgrade": true, "te": true, "trailer": true,
	// The delivery identity and signature headers are set by the dispatcher.
	"x-webhook-id": true, "x-webhook-timestamp": true, "x-webhook-signature": true,
}

func safeAuthHeaderName(value string) bool {
	if value == "" || forbiddenAuthHeaders[strings.ToLower(value)] || strings.HasPrefix(strings.ToLower(value), "proxy-") {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}
