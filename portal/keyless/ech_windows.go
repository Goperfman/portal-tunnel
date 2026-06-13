//go:build windows

package keyless

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gosuda/portal-tunnel/v2/utils"
)

func EncryptedClientHelloMaterials(seed, publicName string) ([]tls.EncryptedClientHelloKey, []byte, error) {
	publicName = utils.NormalizeHostname(publicName)
	if publicName == "" {
		return nil, nil, errors.New("ech public name is required")
	}
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, nil, errors.New("ech seed is required")
	}

	if len(publicName) > echMaximumNameLength {
		return nil, nil, errors.New("ech public name is too long")
	}

	privateKey, err := hkdf.Key(sha256.New, []byte(seed), nil, echHKDFInfoPrefix+publicName, echX25519PrivateLength)
	if err != nil {
		return nil, nil, fmt.Errorf("derive ech private key: %w", err)
	}
	key, err := ecdh.X25519().NewPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ech private key: %w", err)
	}
	publicKey := key.PublicKey().Bytes()
	configID := sha256.Sum256(bytes.Join([][]byte{
		[]byte("portal relay ech config id v1"),
		[]byte(publicName),
		publicKey,
	}, []byte{0}))[0]

	writeUint16 := func(buf *bytes.Buffer, value uint16) {
		var out [2]byte
		binary.BigEndian.PutUint16(out[:], value)
		buf.Write(out[:])
	}
	writeUint16LengthPrefixed := func(buf *bytes.Buffer, data []byte) {
		writeUint16(buf, uint16(len(data)))
		buf.Write(data)
	}

	var body bytes.Buffer
	body.WriteByte(configID)
	writeUint16(&body, echKEMX25519)
	writeUint16LengthPrefixed(&body, publicKey)

	var cipherSuites bytes.Buffer
	writeUint16(&cipherSuites, echKDFHKDFSHA256)
	writeUint16(&cipherSuites, echAEADAES128GCM)
	writeUint16LengthPrefixed(&body, cipherSuites.Bytes())

	body.WriteByte(echMaximumNameLength)
	body.WriteByte(byte(len(publicName)))
	body.WriteString(publicName)
	writeUint16(&body, 0)

	var out bytes.Buffer
	writeUint16(&out, echConfigVersion)
	writeUint16LengthPrefixed(&out, body.Bytes())

	keys := []tls.EncryptedClientHelloKey{{
		Config:      utils.CloneBytes(out.Bytes()),
		PrivateKey:  privateKey,
		SendAsRetry: true,
	}}

	var configList bytes.Buffer
	var configListLength [2]byte
	binary.BigEndian.PutUint16(configListLength[:], uint16(len(keys[0].Config)))
	configList.Write(configListLength[:])
	configList.Write(keys[0].Config)

	return keys, utils.CloneBytes(configList.Bytes()), nil
}
