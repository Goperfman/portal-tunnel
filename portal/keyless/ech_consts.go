package keyless

import (
	"crypto/tls"
	"encoding/binary"
	"errors"

	"github.com/gosuda/portal-tunnel/v2/utils"
)

const (
	echConfigVersion       = 0xfe0d
	echKEMX25519           = 0x0020
	echKDFHKDFSHA256       = 0x0001
	echAEADAES128GCM       = 0x0001
	echMaximumNameLength   = 255
	echMaxConfigListLength = 4096
	echX25519PrivateLength = 32
	echHKDFInfoPrefix      = "portal relay ech v1:"
)

// MinTLSVersion returns the minimum TLS version required when ECH is enabled.
func MinTLSVersion(echEnabled bool) uint16 {
	if echEnabled {
		return tls.VersionTLS13
	}
	return tls.VersionTLS12
}

// NormalizeEncryptedClientHelloConfigList validates and returns a copy of the
// raw ECH config list.
func NormalizeEncryptedClientHelloConfigList(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("ech config list is required")
	}
	if len(raw) > echMaxConfigListLength {
		return nil, errors.New("ech config list is too large")
	}
	if len(raw) < 2 {
		return nil, errors.New("ech config list is invalid")
	}
	listLength := int(binary.BigEndian.Uint16(raw[:2]))
	if listLength != len(raw)-2 {
		return nil, errors.New("ech config list length prefix is invalid")
	}
	return utils.CloneBytes(raw), nil
}
