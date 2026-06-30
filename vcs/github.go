package vcs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const githubAPIBase = "https://api.github.com"

// GitHub is a minimal REST client for the operations the plugin needs: reading
// pull requests and their diffs, opening pull requests, and posting reviews.
type GitHub struct {
	token string
	http  *http.Client
	base  string
}

// NewGitHub returns a GitHub client. An empty token still allows unauthenticated
// access to public resources, subject to GitHub rate limits.
func NewGitHub(token string) *GitHub {
	return &GitHub{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
		base:  githubAPIBase,
	}
}

// Repo identifies a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// PullRequest is the subset of GitHub PR fields the plugin uses.
type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *GitHub) do(ctx context.Context, method, path string, body any, accept string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// GetPR fetches a pull request.
func (c *GitHub) GetPR(ctx context.Context, repo Repo, number int) (*PullRequest, error) {
	data, status, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, number), nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github get PR (status %d): %s", status, string(data))
	}
	var pr PullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetPRDiff fetches the unified diff for a pull request.
func (c *GitHub) GetPRDiff(ctx context.Context, repo Repo, number int) (string, error) {
	data, status, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", repo.Owner, repo.Name, number), nil, "application/vnd.github.v3.diff")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("github get PR diff (status %d): %s", status, string(data))
	}
	return string(data), nil
}

// CreatePRInput is the payload for opening a pull request.
type CreatePRInput struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// CreatePR opens a pull request and returns it.
func (c *GitHub) CreatePR(ctx context.Context, repo Repo, in CreatePRInput) (*PullRequest, error) {
	data, status, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", repo.Owner, repo.Name), in, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, fmt.Errorf("github create PR (status %d): %s", status, string(data))
	}
	var pr PullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// CreateReview posts a review on a pull request. event is one of COMMENT,
// APPROVE, or REQUEST_CHANGES.
func (c *GitHub) CreateReview(ctx context.Context, repo Repo, number int, body, event string) error {
	payload := map[string]string{"body": body, "event": event}
	data, status, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", repo.Owner, repo.Name, number), payload, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("github create review (status %d): %s", status, string(data))
	}
	return nil
}

var (
	repoURLRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$`)
	prURLRe   = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)/pull/(\d+)`)
)

// ParseRepoURL extracts owner and repo from a GitHub repository URL (https or
// ssh form).
func ParseRepoURL(raw string) (Repo, error) {
	m := repoURLRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Repo{}, fmt.Errorf("could not parse GitHub repo URL: %q", raw)
	}
	return Repo{Owner: m[1], Name: strings.TrimSuffix(m[2], ".git")}, nil
}

// ParsePRURL extracts owner, repo, and PR number from a GitHub pull request URL.
func ParsePRURL(raw string) (Repo, int, error) {
	m := prURLRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Repo{}, 0, fmt.Errorf("could not parse GitHub PR URL: %q", raw)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return Repo{}, 0, fmt.Errorf("invalid PR number in %q: %w", raw, err)
	}
	return Repo{Owner: m[1], Name: m[2]}, n, nil
}

// CloneURL returns the canonical https clone URL for a repo.
func (r Repo) CloneURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", r.Owner, r.Name)
}

// String renders the repo as owner/name.
func (r Repo) String() string { return r.Owner + "/" + r.Name }
