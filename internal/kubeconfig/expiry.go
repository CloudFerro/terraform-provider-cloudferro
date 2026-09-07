package kubeconfig

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"time"
)

// ClientCertNotAfter returns the expiry of a client-certificate-data entry of a
// kubeconfig, which holds a base64 encoded PEM certificate.
func ClientCertNotAfter(clientCertData string) (time.Time, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(clientCertData)
	if err != nil {
		return time.Time{}, err
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return time.Time{}, errors.New("client certificate is not a pem block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}

	return cert.NotAfter, nil
}
