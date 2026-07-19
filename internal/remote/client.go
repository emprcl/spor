package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrNotFound is returned when the server does not hold a requested blob.
var ErrNotFound = errors.New("not found")

// ConflictError reports a rejected push: the server moved on since the client
// last synced, so another machine has work this one has not seen. Generation is
// where the server actually is.
type ConflictError struct {
	Generation int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("server is at generation %d; pull first", e.Generation)
}

// maxErrBody caps how much of an error response is quoted back, so a server
// returning an HTML page does not flood the terminal.
const maxErrBody = 512

// Client talks to one project on one spor server.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New builds a client for projectID on the server at rawURL. token may be empty
// for a server that does not require auth.
//
// No overall timeout is set on the HTTP client: a push can legitimately stream
// gigabytes. Cancellation is the caller's context.
func New(rawURL, projectID, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parsing remote url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("remote url %q must be http or https", rawURL)
	}
	if projectID == "" {
		return nil, errors.New("remote: empty project id")
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{base: u.JoinPath("p", projectID), token: token, http: hc}, nil
}

// do issues a request against the project's namespace, applying auth.
func (c *Client) do(ctx context.Context, method string, body io.Reader, parts ...string) (*http.Response, error) {
	u := c.base.JoinPath(parts...)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, u.Redacted(), err)
	}
	return resp, nil
}

// errorFor turns a non-2xx response into an error, quoting a bounded slice of
// the body. It closes the response.
func errorFor(resp *http.Response) error {
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	msg := strings.TrimSpace(string(snippet))
	if msg == "" {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return fmt.Errorf("server returned %s: %s", resp.Status, msg)
}

// Graph fetches the project's current state graph. A server that has never seen
// this project returns generation 0 and no states, which is what a first push
// compares against.
func (c *Client) Graph(ctx context.Context) (Graph, error) {
	resp, err := c.do(ctx, http.MethodGet, nil, "graph")
	if err != nil {
		return Graph{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return Graph{Generation: 0, States: nil}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Graph{}, errorFor(resp)
	}
	defer resp.Body.Close()

	var g Graph
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return Graph{}, fmt.Errorf("decoding graph: %w", err)
	}
	return g, nil
}

// PushGraph swaps the server's graph for states, but only if the server is still
// at baseGeneration. That compare-and-swap is what stops one machine's push from
// silently overwriting another's work; a mismatch comes back as *ConflictError.
//
// It returns the new server generation.
func (c *Client) PushGraph(ctx context.Context, baseGeneration int64, states []State) (int64, error) {
	body, err := json.Marshal(PushRequest{BaseGeneration: baseGeneration, States: states})
	if err != nil {
		return 0, err
	}
	resp, err := c.do(ctx, http.MethodPut, bytes.NewReader(body), "graph")
	if err != nil {
		return 0, err
	}

	if resp.StatusCode == http.StatusConflict {
		defer resp.Body.Close()
		var pr PushResponse
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return 0, fmt.Errorf("decoding conflict response: %w", err)
		}
		return 0, &ConflictError{Generation: pr.Generation}
	}
	if resp.StatusCode != http.StatusOK {
		return 0, errorFor(resp)
	}
	defer resp.Body.Close()

	var pr PushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, fmt.Errorf("decoding push response: %w", err)
	}
	return pr.Generation, nil
}

// MissingBlobs asks which of hashes the server lacks, so an upload costs one
// round-trip instead of one per blob.
func (c *Client) MissingBlobs(ctx context.Context, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(MissingRequest{Hashes: hashes})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, bytes.NewReader(body), "blobs", "missing")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFor(resp)
	}
	defer resp.Body.Close()

	var mr MissingResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding missing-blob response: %w", err)
	}
	return mr.Missing, nil
}

// PutBlob uploads one blob's plaintext bytes under its content hash.
func (c *Client) PutBlob(ctx context.Context, hash string, r io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, r, "blobs", hash)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return errorFor(resp)
	}
	resp.Body.Close()
	return nil
}

// GetBlob streams one blob's plaintext bytes. The caller closes the reader.
func (c *Client) GetBlob(ctx context.Context, hash string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, nil, "blobs", hash)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("blob %s: %w", hash, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errorFor(resp)
	}
	return resp.Body, nil
}
