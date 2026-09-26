// Package proxmox provides a REST API client for Proxmox VE.
package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to one Proxmox VE node or cluster endpoint over the REST API.
type Client struct {
	Endpoint   string
	HTTPClient *http.Client

	ticket      string
	csrf        string
	authToken   string
	tokenSecret string
}

// NewClient creates a new Proxmox VE API client for the given endpoint.
func NewClient(endpoint string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		Endpoint:   strings.TrimSuffix(endpoint, "/"),
		HTTPClient: httpClient,
	}
}

type ticketResponse struct {
	Ticket              string `json:"ticket"`
	CSRFPreventionToken string `json:"CSRFPreventionToken"`
}

// Login authenticates against the Proxmox VE API and sets up the session ticket and CSRF token.
func (c *Client) Login(ctx context.Context, realm, username, password string) error {
	form := url.Values{
		"username": {fmt.Sprintf("%s@%s", username, realm)},
		"password": {password},
	}
	var resp ticketResponse
	if err := c.doForm(ctx, http.MethodPost, "/api2/json/access/ticket", form, &resp); err != nil {
		return err
	}
	c.ticket = resp.Ticket
	c.csrf = resp.CSRFPreventionToken
	c.authToken = ""
	c.tokenSecret = ""
	return nil
}

// SetAPIToken configures API token authentication using the provided credentials.
func (c *Client) SetAPIToken(user, tokenID, secret string) {
	c.authToken = fmt.Sprintf("PVEAPIToken=%s!%s=%s", user, tokenID, secret)
	c.tokenSecret = secret
	c.ticket = ""
	c.csrf = ""
}

func (c *Client) redact(s string) string {
	if c.ticket != "" {
		s = strings.ReplaceAll(s, c.ticket, "<redacted>")
	}
	if c.csrf != "" {
		s = strings.ReplaceAll(s, c.csrf, "<redacted>")
	}
	if c.authToken != "" {
		s = strings.ReplaceAll(s, c.authToken, "PVEAPIToken=<redacted>")
	}
	if c.tokenSecret != "" {
		s = strings.ReplaceAll(s, c.tokenSecret, "<redacted>")
	}
	return s
}

type envelope struct {
	Data json.RawMessage `json:"data"`
}

type errorEnvelope struct {
	Message string            `json:"message"`
	Errors  map[string]string `json:"errors"`
}

func (c *Client) authenticate(req *http.Request) {
	switch {
	case c.authToken != "":
		req.Header.Set("Authorization", c.authToken)
	case c.ticket != "":
		req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: c.ticket})
		if req.Method != http.MethodGet {
			req.Header.Set("CSRFPreventionToken", c.csrf)
		}
	}
}

func (c *Client) doForm(ctx context.Context, method, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("proxmox: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("proxmox: encode body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("proxmox: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	c.authenticate(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox: %s %s: %w", req.Method, c.redact(req.URL.Path), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("proxmox: %s %s: read response: %w", req.Method, c.redact(req.URL.Path), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.apiError(resp.StatusCode, body)
	}
	if out == nil {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("proxmox: %s %s: decode envelope: %w", req.Method, c.redact(req.URL.Path), err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("proxmox: %s %s: decode data: %w", req.Method, c.redact(req.URL.Path), err)
	}
	return nil
}

func (c *Client) apiError(status int, body []byte) error {
	var ee errorEnvelope
	message := c.redact(strings.TrimSpace(string(body)))
	if json.Unmarshal(body, &ee) == nil {
		if ee.Message != "" {
			message = c.redact(ee.Message)
		} else if len(ee.Errors) > 0 {
			parts := make([]string, 0, len(ee.Errors))
			for k, v := range ee.Errors {
				parts = append(parts, fmt.Sprintf("%s: %s", k, v))
			}
			message = c.redact(strings.Join(parts, "; "))
		}
	}
	return &APIError{Status: status, Message: message}
}

// Get performs an authenticated GET request to path and decodes into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodGet, p, nil, out)
}

// Post performs an authenticated POST request with body to path and decodes into out.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodPost, p, body, out)
}

// Put performs an authenticated PUT request with body to path and decodes into out.
func (c *Client) Put(ctx context.Context, path string, body any, out any) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodPut, p, body, out)
}

// Delete performs an authenticated DELETE request to path.
func (c *Client) Delete(ctx context.Context, path string) error {
	p := path
	if !strings.HasPrefix(p, "/api2/json") {
		p = "/api2/json" + path
	}
	return c.doJSON(ctx, http.MethodDelete, p, nil, nil)
}
