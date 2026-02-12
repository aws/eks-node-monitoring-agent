package net

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"time"
)

type RequestOpts = func(*http.Client)

func DoRequest(req *http.Request, fnOpts ...RequestOpts) (io.ReadCloser, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	for _, fn := range fnOpts {
		fn(client)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read body of response due to: %s", err)
		}
		return nil, fmt.Errorf("request failed, response body: %q", string(body))
	}

	return res.Body, nil
}

func WithCaCert(caCertData []byte) RequestOpts {
	return func(c *http.Client) {
		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}

		rootCAs.AppendCertsFromPEM(caCertData)

		c.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootCAs,
				MinVersion: tls.VersionTLS12,
			},
		}
	}
}
