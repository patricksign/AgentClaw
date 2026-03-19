// Package github provides a GitHub REST API client for the AgentClaw pipeline.
// It covers the PR lifecycle: branch creation, PR creation (draft), review
// submission (APPROVE / REQUEST_CHANGES), merge, and diff fetching.
//
// Required env vars:
//
//	GITHUB_TOKEN  — personal access token or fine-grained PAT with repo scope
//	GITHUB_OWNER  — repository owner (user or org)
//	GITHUB_REPO   — repository name (without .git)
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	apiBase          = "https://api.github.com"
	apiVersion       = "2022-11-28"
	maxResponseBytes = 10 << 20 // 10 MiB
)

// Client is a GitHub REST API client.
type Client struct {
	token  string
	owner  string
	repo   string
	http   *http.Client
}

// newTransport returns an isolated, IPv4-only transport.
func newTransport() *http.Transport {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       90 * time.Second,
	}
}

// New creates a Client from environment variables.
// Returns an error if any required variable is missing.
func New() (*Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	owner := os.Getenv("GITHUB_OWNER")
	repo := os.Getenv("GITHUB_REPO")
	if token == "" {
		return nil, fmt.Errorf("github: GITHUB_TOKEN is not set")
	}
	if owner == "" {
		return nil, fmt.Errorf("github: GITHUB_OWNER is not set")
	}
	if repo == "" {
		return nil, fmt.Errorf("github: GITHUB_REPO is not set")
	}
	return &Client{
		token: token,
		owner: owner,
		repo:  repo,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: newTransport(),
		},
	}, nil
}

// IsConfigured reports whether all three required env vars are present.
func IsConfigured() bool {
	return os.Getenv("GITHUB_TOKEN") != "" &&
		os.Getenv("GITHUB_OWNER") != "" &&
		os.Getenv("GITHUB_REPO") != ""
}

// ─── Response types ──────────────────────────────────────────────────────────

// Branch holds a reference to a GitHub branch.
type Branch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// PullRequest is the minimal PR representation returned by the API.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
}

// Review is the review submission response.
type Review struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Body  string `json:"body"`
}

// MergeResult is returned by MergePR.
type MergeResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// ─── Branch operations ───────────────────────────────────────────────────────

// GetDefaultBranchSHA returns the HEAD commit SHA of branch.
// GET /repos/{owner}/{repo}/branches/{branch}
func (c *Client) GetDefaultBranchSHA(ctx context.Context, branch string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/branches/%s", apiBase, c.owner, c.repo, branch)
	raw, err := c.get(ctx, endpoint)
	if err != nil {
		return "", fmt.Errorf("github: GetDefaultBranchSHA: %w", err)
	}
	var b Branch
	if err := json.Unmarshal(raw, &b); err != nil {
		return "", fmt.Errorf("github: parse branch: %w", err)
	}
	return b.Commit.SHA, nil
}

// CreateBranch creates a new branch from fromSHA.
// POST /repos/{owner}/{repo}/git/refs
func (c *Client) CreateBranch(ctx context.Context, branchName, fromSHA string) error {
	body := map[string]string{
		"ref": "refs/heads/" + branchName,
		"sha": fromSHA,
	}
	_, err := c.post(ctx, fmt.Sprintf("%s/repos/%s/%s/git/refs", apiBase, c.owner, c.repo), body)
	if err != nil {
		return fmt.Errorf("github: CreateBranch %q: %w", branchName, err)
	}
	return nil
}

// ─── Pull request operations ─────────────────────────────────────────────────

// CreatePR creates a pull request. Set draft=true for a draft PR.
// POST /repos/{owner}/{repo}/pulls
func (c *Client) CreatePR(ctx context.Context, title, head, base, body string, draft bool) (*PullRequest, error) {
	payload := map[string]any{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
		"draft": draft,
	}
	raw, err := c.post(ctx, fmt.Sprintf("%s/repos/%s/%s/pulls", apiBase, c.owner, c.repo), payload)
	if err != nil {
		return nil, fmt.Errorf("github: CreatePR: %w", err)
	}
	var pr PullRequest
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("github: parse PR: %w", err)
	}
	return &pr, nil
}

// CreateFeaturePR is the full flow:
//  1. Get the HEAD SHA of baseBranch.
//  2. Create a feature branch named feature/{taskID}-{sanitized-title}.
//  3. Create a draft PR from that branch into baseBranch.
func (c *Client) CreateFeaturePR(ctx context.Context, taskID, taskTitle, baseBranch, prBody string) (*PullRequest, error) {
	sha, err := c.GetDefaultBranchSHA(ctx, baseBranch)
	if err != nil {
		return nil, err
	}

	branchName := "feature/" + taskID + "-" + sanitizeBranchName(taskTitle)
	if err := c.CreateBranch(ctx, branchName, sha); err != nil {
		return nil, err
	}

	return c.CreatePR(ctx, taskTitle, branchName, baseBranch, prBody, true)
}

// SubmitReview submits a review on a pull request.
// event must be "APPROVE" or "REQUEST_CHANGES".
// POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews
func (c *Client) SubmitReview(ctx context.Context, prNumber int, event, body string) (*Review, error) {
	payload := map[string]string{
		"body":  body,
		"event": event,
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", apiBase, c.owner, c.repo, prNumber)
	raw, err := c.post(ctx, endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("github: SubmitReview PR#%d: %w", prNumber, err)
	}
	var review Review
	if err := json.Unmarshal(raw, &review); err != nil {
		return nil, fmt.Errorf("github: parse review: %w", err)
	}
	return &review, nil
}

// MergePR merges a pull request using the squash method by default.
// PUT /repos/{owner}/{repo}/pulls/{pull_number}/merge
func (c *Client) MergePR(ctx context.Context, prNumber int, commitTitle string) (*MergeResult, error) {
	payload := map[string]string{
		"merge_method": "squash",
		"commit_title": commitTitle,
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/merge", apiBase, c.owner, c.repo, prNumber)
	raw, err := c.put(ctx, endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("github: MergePR #%d: %w", prNumber, err)
	}
	var result MergeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("github: parse merge result: %w", err)
	}
	return &result, nil
}

// GetPRDiff returns the raw unified diff of a pull request.
// GET /repos/{owner}/{repo}/pulls/{pull_number}  Accept: application/vnd.github.diff
func (c *Client) GetPRDiff(ctx context.Context, prNumber int) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, c.owner, c.repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("github: build request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "application/vnd.github.diff")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("github: read diff: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: GetPRDiff %d: %s", resp.StatusCode, raw)
	}
	return string(raw), nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	return c.do(req)
}

func (c *Client) post(ctx context.Context, url string, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	return c.do(req)
}

func (c *Client) put(ctx context.Context, url string, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%d: %s", resp.StatusCode, raw)
	}
	return raw, nil
}

// sanitizeBranchName converts a task title into a URL-safe branch segment.
// Non-alphanumeric characters become hyphens; consecutive hyphens are collapsed.
var reNonAlpha = regexp.MustCompile(`[^a-z0-9]+`)

func sanitizeBranchName(s string) string {
	s = strings.ToLower(s)
	s = reNonAlpha.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
