// Package trello — extended Trello REST client for the AgentClaw pipeline.
// This file adds the higher-level helpers (EnsureChecklist, PopulateChecklist,
// SetCheckItemState, UpdateCardDescription) needed by the pipeline orchestrator.
// The low-level HTTP primitives (Card, Checklist, CheckItem, etc.) live in
// client.go which remains unchanged.
package trello

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"log/slog"
)

// IsConfigured reports whether both TRELLO_KEY and TRELLO_TOKEN are embedded in
// the client. A nil Client always returns false.
func (c *Client) IsConfigured() bool {
	return c != nil && c.apiKey != "" && c.token != ""
}

// UpdateCardDescription replaces (appends to) the card description.
// PUT /1/cards/{id}   — sets the desc field to newDesc.
func (c *Client) UpdateCardDescription(ctx context.Context, cardID, newDesc string) error {
	params := url.Values{}
	params.Set("desc", newDesc)

	endpoint := fmt.Sprintf("%s/cards/%s?%s", baseURL, cardID, params.Encode())
	req, err := newPUT(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return fmt.Errorf("trello: UpdateCardDescription %d", resp.StatusCode)
	}
	return nil
}

// GetCardChecklists returns all checklists on a card.
// GET /1/cards/{id}/checklists
func (c *Client) GetCardChecklists(ctx context.Context, cardID string) ([]Checklist, error) {
	endpoint := fmt.Sprintf("%s/cards/%s/checklists", baseURL, cardID)
	req, err := newGET(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("trello: read response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("trello: GetCardChecklists %d: %s", resp.StatusCode, raw)
	}
	var lists []Checklist
	return lists, unmarshalJSON(raw, &lists)
}

// EnsureChecklist finds an existing checklist named "AgentClaw Tasks" on the
// card, or creates one if none exists. Returns the checklist (existing or new).
func (c *Client) EnsureChecklist(ctx context.Context, cardID string) (*Checklist, error) {
	lists, err := c.GetCardChecklists(ctx, cardID)
	if err != nil {
		return nil, err
	}
	for i := range lists {
		if lists[i].Name == "AgentClaw Tasks" {
			return &lists[i], nil
		}
	}
	return c.CreateChecklist(ctx, cardID, "AgentClaw Tasks")
}

// PopulateChecklist bulk-adds items to a checklist and returns a map of
// title → checkItemID for each item that was successfully created.
// Items that fail to create are logged and skipped; the map only contains
// successfully created items.
func (c *Client) PopulateChecklist(ctx context.Context, checklistID string, items []string) (map[string]string, error) {
	result := make(map[string]string, len(items))
	for _, title := range items {
		item, err := c.AddCheckItem(ctx, checklistID, title)
		if err != nil {
			slog.Warn("trello: PopulateChecklist: failed to add check item", "err", err, "title", title)
			continue
		}
		result[title] = item.ID
	}
	return result, nil
}

// SetCheckItemState marks a checklist item complete (true) or incomplete (false).
// PUT /1/cards/{cardID}/checkItem/{checkItemID}
func (c *Client) SetCheckItemState(ctx context.Context, cardID, checkItemID string, complete bool) error {
	state := "incomplete"
	if complete {
		state = "complete"
	}

	params := url.Values{}
	params.Set("state", state)

	endpoint := fmt.Sprintf("%s/cards/%s/checkItem/%s?%s", baseURL, cardID, checkItemID, params.Encode())
	req, err := newPUT(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return fmt.Errorf("trello: SetCheckItemState %d", resp.StatusCode)
	}
	return nil
}

// DeleteCheckItem removes a check item from a checklist.
// DELETE /1/cards/{id}/checkItem/{idCheckItem}
func (c *Client) DeleteCheckItem(ctx context.Context, cardID, checkItemID string) error {
	endpoint := fmt.Sprintf("%s/cards/%s/checkItem/%s", baseURL, cardID, checkItemID)
	req, err := newDELETE(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return fmt.Errorf("trello: DeleteCheckItem %d", resp.StatusCode)
	}
	return nil
}

// DeleteChecklist removes an entire checklist from a card.
// DELETE /1/cards/{id}/checklists/{idChecklist}
func (c *Client) DeleteChecklist(ctx context.Context, cardID, checklistID string) error {
	endpoint := fmt.Sprintf("%s/cards/%s/checklists/%s", baseURL, cardID, checklistID)
	req, err := newDELETE(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != 200 {
		return fmt.Errorf("trello: DeleteChecklist %d", resp.StatusCode)
	}
	return nil
}

// CreateList creates a new list on a board.
// POST /1/lists
func (c *Client) CreateList(ctx context.Context, boardID, name string) (*List, error) {
	params := url.Values{}
	params.Set("idBoard", boardID)
	params.Set("name", name)

	endpoint := fmt.Sprintf("%s/lists?%s", baseURL, params.Encode())
	req, err := newPOST(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("trello: build request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trello: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("trello: read response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("trello: CreateList %d: %s", resp.StatusCode, raw)
	}
	var list List
	return &list, unmarshalJSON(raw, &list)
}
