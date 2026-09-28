package ai

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// setRequestHeaders sets each header on request; a "Host" entry sets the
// request's Host instead.
func setRequestHeaders(request *http.Request, headers map[string]string) {
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			request.Host = v
		} else {
			request.Header.Set(k, v)
		}
	}
}

// doStream sends a streaming request and returns the response when it is
// 200 OK. Any other status reads and closes the body into the error.
func doStream(client *http.Client, request *http.Request, label string) (*http.Response, error) {
	resp, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", label, err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d: %s", label, resp.StatusCode, string(b))
	}
	return resp, nil
}

func applyProviderHeaders(request *http.Request, headers ProviderHeaders) {
	for name, value := range headers {
		if strings.EqualFold(name, "host") {
			if value == nil {
				request.Host = ""
			} else {
				request.Host = *value
			}
			continue
		}
		if value == nil {
			request.Header.Del(name)
			continue
		}
		request.Header.Set(name, *value)
	}
}
