package transport

import (
	"net"
	"net/http"
	"time"
)

var directTransportInstance *http.Transport

func directTransport() *http.Transport {
	if directTransportInstance != nil {
		return directTransportInstance
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	directTransportInstance = transport
	return transport
}

func DirectTransport() *http.Transport { return directTransport() }
