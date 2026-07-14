//go:build !windows

package keyless

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gosuda/beaver/alloc"

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

	a := alloc.NewArena()
	defer a.Close()
	ctx := alloc.WithAllocator(context.Background(), a)

	writeUint16 := func(buf io.Writer, value uint16) {
		var out [2]byte
		binary.BigEndian.PutUint16(out[:], value)
		_, _ = buf.Write(out[:])
	}
	writeUint16LengthPrefixed := func(buf io.Writer, data []byte) {
		writeUint16(buf, uint16(len(data)))
		_, _ = buf.Write(data)
	}

	body := alloc.NewBuffer(ctx)
	_, _ = body.Write([]byte{configID})
	writeUint16(body, echKEMX25519)
	writeUint16LengthPrefixed(body, publicKey)

	cipherSuites := alloc.NewBuffer(ctx)
	writeUint16(cipherSuites, echKDFHKDFSHA256)
	writeUint16(cipherSuites, echAEADAES128GCM)
	writeUint16LengthPrefixed(body, cipherSuites.Bytes())

	_, _ = body.Write([]byte{echMaximumNameLength})
	_, _ = body.Write([]byte{byte(len(publicName))})
	_, _ = body.Write([]byte(publicName))
	writeUint16(body, 0)

	out := alloc.NewBuffer(ctx)
	writeUint16(out, echConfigVersion)
	writeUint16LengthPrefixed(out, body.Bytes())

	config := utils.CloneBytes(out.Bytes())
	keys := []tls.EncryptedClientHelloKey{{
		Config:      config,
		PrivateKey:  privateKey,
		SendAsRetry: true,
	}}

	configList := make([]byte, 2+len(config))
	binary.BigEndian.PutUint16(configList[:2], uint16(len(config)))
	copy(configList[2:], config)

	return keys, configList, nil
}
