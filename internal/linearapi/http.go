// Package linearapi supplies the shared, read-only Linear HTTP transport.
package linearapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

const Endpoint = "https://api.linear.app/graphql"

type HTTPDo func(*http.Request) (*http.Response, error)

// Query deliberately returns no upstream error text: URLs and response bodies
// can contain credentials. Callers map the status to their own safe messages.
func Query(ctx context.Context, do HTTPDo, endpoint, key string, payload []byte) ([]byte, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")
	if do == nil {
		do = (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do
	}
	resp, err := do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0
	}
	return body, resp.StatusCode
}
