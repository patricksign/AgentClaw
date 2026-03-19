// Package trello provides a minimal Trello REST API client for creating cards.
// Only the operations needed by the breakdown agent are implemented.
//
// Authentication: Trello uses API Key + Token.
//   - Get your API key: https://trello.com/app-key
//   - Generate a token:  https://trello.com/1/authorize?expiration=never&scope=read,write&response_type=token&key=YOUR_KEY
//
// Required env vars (set per-agent via Config.Env):
//
//	TRELLO_API_KEY  — your Trello Power-Up API key
//	TRELLO_TOKEN    — your user token (never-expiring recommended for automation)
//	TRELLO_LIST_ID  — the ID of the list (column) to create cards in
//
// Find your list ID:
//
//	curl "https://api.trello.com/1/boards/{BOARD_ID}/lists?key=KEY&token=TOKEN"
package trello

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	baseURL          = "https://api.trello.com/1"
	maxResponseBytes = 1 << 20 // 1 MiB
)

// Client is a minimal Trello API client.
type Client struct {
	apiKey string
	token  string
	http   *http.Client
}

// setAuth sets the OAuth Authorization header on the request and removes
// key/token from the query string (if present). All Trello requests must
// use this instead of embedding credentials in query parameters.
func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("Authorization",
		fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, c.apiKey, c.token))
	// Strip key/token from query string if accidentally set.
	q := req.URL.Query()
	q.Del("key")
	q.Del("token")
	req.URL.RawQuery = q.Encode()
}

// newTransport returns an isolated http.Transport that dials IPv4 only.
// Forces tcp4 to avoid IPv6 connectivity issues on networks where AAAA
// records resolve but the path is broken.
func newTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        5,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     60 * time.Second,
	}
}

// New creates a Client. Returns an error if apiKey or token is empty.
func New(apiKey, token string) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("trello: TRELLO_API_KEY is not set")
	}
	if token == "" {
		return nil, fmt.Errorf("trello: TRELLO_TOKEN is not set")
	}
	return &Client{
		apiKey: apiKey,
		token:  token,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: newTransport(),
		},
	}, nil
}

// Card is the data needed to create a Trello card.
type Card struct {
	Name        string // card title (required)
	Description string // card body / acceptance criteria
	ListID      string // destination list ID (required)
	Position    string // "top" | "bottom" (default: "bottom")
}

// CreatedCard is the Trello API response after card creation.
type CreatedCard struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ShortURL  string `json:"shortUrl"`
	ShortLink string `json:"shortLink"`
	IDList    string `json:"idList"`
}

// CreateCard creates a new card on the specified list.
// Returns the created card including its URL.
func (c *Client) CreateCard(ctx context.Context, card Card) (*CreatedCard, error) {
	if card.Name == "" {
		return nil, fmt.Errorf("trello: card name is required")
	}
	if card.ListID == "" {
		return nil, fmt.Errorf("trello: card ListID is required")
	}
	if card.Position == "" {
		card.Position = "bottom"
	}

	params := url.Values{}
	params.Set("idList", card.ListID)
	params.Set("name", card.Name)
	params.Set("desc", card.Description)
	params.Set("pos", card.Position)

	endpoint := baseURL + "/cards?" + params.Encode()
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var created CreatedCard
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return &created, nil
}

// List represents a Trello list (column on a board).
type List struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BoardCard is a card returned by the board cards endpoint.
type BoardCard struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Desc         string   `json:"desc"`
	ShortURL     string   `json:"shortUrl"`
	IDList       string   `json:"idList"`
	Labels       []Label  `json:"labels"`
	IDLabelNames []string `json:"-"` // populated from Labels
}

// Label is a Trello label attached to a card.
type Label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GetBoardCards returns all open (non-archived) cards on a board.
func (c *Client) GetBoardCards(ctx context.Context, boardID string) ([]BoardCard, error) {
	params := url.Values{}
	params.Set("filter", "open")
	params.Set("fields", "id,name,desc,shortUrl,idList,labels")

	endpoint := fmt.Sprintf("%s/boards/%s/cards?%s", baseURL, boardID, params.Encode())
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var cards []BoardCard
	if err := json.Unmarshal(raw, &cards); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return cards, nil
}

// MoveCard moves a card to a different list (e.g. from "Ideas" to "Processing").
func (c *Client) MoveCard(ctx context.Context, cardID, targetListID string) error {
	params := url.Values{}
	params.Set("idList", targetListID)

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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trello: MoveCard API %d", resp.StatusCode)
	}
	return nil
}

// GetBoardLists returns all lists on a board — useful to find the target list ID.
func (c *Client) GetBoardLists(ctx context.Context, boardID string) ([]List, error) {
	endpoint := fmt.Sprintf("%s/boards/%s/lists", baseURL, boardID)
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var lists []List
	if err := json.Unmarshal(raw, &lists); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return lists, nil
}

// ─── Card fetch ───────────────────────────────────────────────────────────────

// GetCard fetches a single card by ID or shortLink.
// Returns nil + error if not found (404).
func (c *Client) GetCard(ctx context.Context, cardID string) (*BoardCard, error) {
	params := url.Values{}
	params.Set("fields", "id,name,desc,shortUrl,idList,labels")

	endpoint := fmt.Sprintf("%s/cards/%s?%s", baseURL, cardID, params.Encode())
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("trello: card %q not found", cardID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var card BoardCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return &card, nil
}

// ─── Checklist operations ─────────────────────────────────────────────────────

// Checklist represents a Trello checklist on a card.
type Checklist struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	IDCard     string      `json:"idCard"`
	CheckItems []CheckItem `json:"checkItems"`
}

// CheckItem represents an item inside a Trello checklist.
type CheckItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"` // "complete" | "incomplete"
	IDChecklist string `json:"idChecklist"`
}

// CreateChecklist creates a new checklist on a card and returns it.
// POST /1/cards/{id}/checklists
func (c *Client) CreateChecklist(ctx context.Context, cardID, name string) (*Checklist, error) {
	params := url.Values{}
	params.Set("name", name)

	endpoint := fmt.Sprintf("%s/cards/%s/checklists?%s", baseURL, cardID, params.Encode())
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var cl Checklist
	if err := json.Unmarshal(raw, &cl); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return &cl, nil
}

// AddCheckItem adds an item to a checklist. Returns the created CheckItem.
// POST /1/checklists/{id}/checkItems
func (c *Client) AddCheckItem(ctx context.Context, checklistID, name string) (*CheckItem, error) {
	params := url.Values{}
	params.Set("name", name)
	params.Set("checked", "false")

	endpoint := fmt.Sprintf("%s/checklists/%s/checkItems?%s", baseURL, checklistID, params.Encode())
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trello: API %d: %s", resp.StatusCode, raw)
	}

	var item CheckItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("trello: parse response: %w", err)
	}
	return &item, nil
}

// CompleteCheckItem marks a checklist item as complete on a card.
// PUT /1/cards/{cardID}/checkItem/{checkItemID}
func (c *Client) CompleteCheckItem(ctx context.Context, cardID, checkItemID string) error {
	params := url.Values{}
	params.Set("state", "complete")

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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("trello: CompleteCheckItem API %d", resp.StatusCode)
	}
	return nil
}

// ─── Ticket parsing ───────────────────────────────────────────────────────────

// Ticket is the structured ticket produced by the breakdown agent LLM output.
type Ticket struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	StoryPoints        int      `json:"story_points"`
	DependsOn          []string `json:"depends_on"`
}

// ParseTickets parses the JSON array of tickets from LLM output.
// The breakdown agent is prompted to return:
//
//	[{"title":"...","description":"...","acceptance_criteria":"...","story_points":3,"depends_on":[]}]
//
// ParseTickets is lenient: it scans for the first '[' and last ']' so leading
// prose or markdown fences around the JSON do not cause failures.
func ParseTickets(llmOutput string) ([]Ticket, error) {
	start := strings.Index(llmOutput, "[")
	end := strings.LastIndex(llmOutput, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("trello: no JSON array found in LLM output")
	}
	raw := llmOutput[start : end+1]

	var tickets []Ticket
	if err := json.Unmarshal([]byte(raw), &tickets); err != nil {
		return nil, fmt.Errorf("trello: parse tickets: %w", err)
	}
	if len(tickets) == 0 {
		return nil, fmt.Errorf("trello: LLM returned empty ticket list")
	}
	return tickets, nil
}

// FormatCardDescription builds the Trello card body from a Ticket.
func FormatCardDescription(t Ticket) string {
	var sb strings.Builder
	if t.Description != "" {
		sb.WriteString(t.Description)
		sb.WriteString("\n\n")
	}
	if t.AcceptanceCriteria != "" {
		sb.WriteString("## Acceptance Criteria\n")
		sb.WriteString(t.AcceptanceCriteria)
		sb.WriteString("\n\n")
	}
	if t.StoryPoints > 0 {
		sb.WriteString(fmt.Sprintf("**Story Points:** %d\n", t.StoryPoints))
	}
	if len(t.DependsOn) > 0 {
		sb.WriteString(fmt.Sprintf("**Depends On:** %s\n", strings.Join(t.DependsOn, ", ")))
	}
	return strings.TrimSpace(sb.String())
}
