package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/plugins/embedding/openai"
)

func TestEmbedContract(t *testing.T) {
	var seen map[string]any
	var auth string
	dims := 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&seen)
		v := make([]float32, dims)
		// Out of order on purpose: results are placed by index.
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 1, "embedding": v}, {"index": 0, "embedding": v}}})
	}))
	defer srv.Close()
	t.Setenv("TEST_EMBED_TOKEN", "secret")
	c, err := openai.New(openai.Config{URL: srv.URL, Model: "m", Dimensions: 3, InputTypeField: "input_type", TokenEnv: "TEST_EMBED_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	vs, err := c.Embed(context.Background(), skills.PurposeQuery, []string{"a", "b"})
	if err != nil || len(vs) != 2 {
		t.Fatal(vs, err)
	}
	if auth != "Bearer secret" || seen["input_type"] != "query" || seen["model"] != "m" {
		t.Fatal("request", auth, seen)
	}
	dims = 2 // a provider changing dimension must fail, never truncate
	if _, err = c.Embed(context.Background(), skills.PurposeQuery, []string{"a", "b"}); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
}

func TestConfigRefusesPlainRemoteHTTP(t *testing.T) {
	if _, err := openai.New(openai.Config{URL: "http://example.com/v1/embeddings", Model: "m", Dimensions: 3}); err == nil {
		t.Fatal("plain http to a remote host accepted")
	}
}
