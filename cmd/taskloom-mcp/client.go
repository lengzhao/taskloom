package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// client calls the shared business API with the agent's Bearer credential.
type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	return &client{base: base, token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// apiError keeps the HTTP status so tools can explain what the model should do.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }

func (c *client) do(ctx context.Context, method, path string, body any, key string, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Wire shapes: only the fields the adapter reads from the shared API.
type commitDTO struct {
	ID, Author, Kind, Body, RefID string
	CreatedAt                     time.Time
	Sequence                      int64
}
type detailDTO struct {
	Task struct {
		ID, Title, Goal, Criteria, Owner string
		Version, BasisVersion            int64
	}
	Commits []commitDTO
}
type executionDTO struct {
	ID, RoundID, Subject, Status string
	Generation, BasisVersion     int64
	LeaseUntil                   time.Time
	Writable, Safe               bool
}
type snapshotDTO struct {
	Position struct {
		Revision                        int64
		Status, State, RoundID, Waiting string
	}
	Assignee, NextActor                      string
	Blocked                                  map[string]string
	CanOpen, CanSubmit, CanRenew, SafeToExit bool
	Execution                                *executionDTO
	Submission                               *struct{ ID, CommitID string }
	ResultCommit                             *commitDTO
	Review                                   *struct{ Outcome, Reason string }
	Questions                                []struct {
		ID, CommitID, Asker, Addressee string
		CreatedAt                      time.Time
	}
}
type pendingDTO struct {
	TaskID, Title, NextActor string
}
