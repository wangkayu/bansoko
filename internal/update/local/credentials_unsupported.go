//go:build !linux && !darwin

package localupdate

import (
	"errors"
	"net"
	"os"
)

const Supported = false

var errPeerCredentialsUnsupported = errors.New("kernel peer credentials are unsupported on this platform")

func peerUID(_ *net.UnixConn) (uint32, error) {
	return 0, errPeerCredentialsUnsupported
}

func fileOwnerUID(_ os.FileInfo) (uint32, error) {
	return 0, errPeerCredentialsUnsupported
}
