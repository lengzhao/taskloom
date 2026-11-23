// Package openai embeds texts through an OpenAI-compatible /embeddings
// endpoint. The bearer token never appears in configuration: it is read
// from the environment variable named by tokenEnv, if any.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Config struct {
	// URL is the full embeddings endpoint, e.g. https://host/v1/embeddings.
	URL   string `json:"url"`
	Model string `json:"model"`
	// Dimensions every vector must have; a mismatch is a provider failure,
	// never truncated, because stored vectors share one space.
	Dimensions int `json:"dimensions"`
	// SendDimensions asks providers that support it for exactly Dimensions.
	SendDimensions bool `json:"sendDimensions"`
	// InputTypeField, when set, sends the purpose (query or document) in this
	// request field for providers that encode the two differently.
	InputTypeField string `json:"inputTypeField"`
	// TokenEnv names the environment variable holding the bearer token.
	TokenEnv string `json:"tokenEnv"`
	Timeout  string `json:"timeout"`
}

func (c *Config) SetDefaults() {
	if c.Timeout == "" {
		c.Timeout = "10s"
	}
}
func (c *Config) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && httpx.LoopbackHost(u.Host))) {
		return errors.New("url must be https, or http on a loopback host")
	}
	if c.Model == "" {
		return errors.New("model required")
	}
	if c.Dimensions < 1 || c.Dimensions > 8192 {
		return errors.New("dimensions must be in [1, 8192]")
	}
	if d, err := time.ParseDuration(c.Timeout); err != nil || d <= 0 {
		return errors.New("timeout must be a positive duration")
	}
	return nil
}

type Client struct {
	c    Config
	http *http.Client
}

func init() { pluginkit.Register("embedding/openai", New) }

func New(c Config) (*Client, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	t, _ := time.ParseDuration(c.Timeout)
	return &Client{c: c, http: &http.Client{Timeout: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

const maxBatch = 64

func (e *Client) Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error) {
	if len(texts) == 0 || len(texts) > maxBatch || (purpose != skills.PurposeQuery && purpose != skills.PurposeDocument) {
		return nil, fault.ErrInput
	}
	body := map[string]any{"model": e.c.Model, "input": texts}
	if e.c.SendDimensions {
		body["dimensions"] = e.c.Dimensions
	}
	if e.c.InputTypeField != "" {
		body[e.c.InputTypeField] = purpose
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.c.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.c.TokenEnv != "" {
		if tok := os.Getenv(e.c.TokenEnv); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fault.ErrUnavailable // never echo provider errors or URLs
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fault.ErrUnavailable
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil || len(out.Data) != len(texts) {
		return nil, fault.ErrUnavailable
	}
	vs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vs[d.Index] != nil || len(d.Embedding) != e.c.Dimensions {
			return nil, fault.ErrUnavailable
		}
		vs[d.Index] = d.Embedding
	}
	return vs, nil
}
