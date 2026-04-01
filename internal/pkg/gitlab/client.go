package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/shared"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any) ([]byte, http.Header, error) {
	var bodyData []byte
	if body != nil {
		var err error
		bodyData, err = json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request body: %w", err)
		}
	}

	url := c.baseURL + "/api/v4" + path

	var resp *http.Response
	var lastErr error
	for attempt := range 3 {
		// Re-create the request on each attempt so the body reader is fresh.
		var bodyReader io.Reader
		if bodyData != nil {
			bodyReader = bytes.NewReader(bodyData)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("PRIVATE-TOKEN", c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("http request: %w", err)
		}

		if resp.StatusCode == 429 {
			retryAfter := resp.Header.Get("Retry-After")
			wait := time.Duration(attempt+1) * 2 * time.Second
			if retryAfter != "" {
				if secs, err := strconv.Atoi(retryAfter); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
			slog.Warn("rate limited, retrying", "wait", wait, "attempt", attempt+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			lastErr = fmt.Errorf("rate limited (HTTP 429)")
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			wait := time.Duration(1<<uint(attempt)) * time.Second
			slog.Warn("server error, retrying", "status", resp.StatusCode, "attempt", attempt+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			lastErr = fmt.Errorf("server error (HTTP %d)", resp.StatusCode)
			continue
		}

		// Success or non-retryable client error — read body and return.
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode >= 400 {
			return nil, nil, fmt.Errorf("gitlab api error %d: %s", resp.StatusCode, string(respBody))
		}
		return respBody, resp.Header, nil
	}

	if lastErr != nil {
		return nil, nil, fmt.Errorf("all retries exhausted: %w", lastErr)
	}
	return nil, nil, fmt.Errorf("all retries exhausted")
}

func (c *Client) GetMR(ctx context.Context, projectID, mrIID int64) (*shared.GitLabMR, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d", projectID, mrIID)
	data, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var raw struct {
		IID          int64  `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		SHA          string `json:"sha"`
		WebURL       string `json:"web_url"`
		Author       struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal MR: %w", err)
	}

	return &shared.GitLabMR{
		IID:            raw.IID,
		Title:          raw.Title,
		Description:    raw.Description,
		SourceBranch:   raw.SourceBranch,
		TargetBranch:   raw.TargetBranch,
		HeadSHA:        raw.SHA,
		WebURL:         raw.WebURL,
		AuthorUsername: raw.Author.Username,
	}, nil
}

func (c *Client) ListMRFiles(ctx context.Context, projectID, mrIID int64) ([]shared.GitLabMRFile, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/changes", projectID, mrIID)
	data, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Changes []struct {
			OldPath     string `json:"old_path"`
			NewPath     string `json:"new_path"`
			NewFile     bool   `json:"new_file"`
			DeletedFile bool   `json:"deleted_file"`
			RenamedFile bool   `json:"renamed_file"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal MR files: %w", err)
	}

	files := make([]shared.GitLabMRFile, len(raw.Changes))
	for i, ch := range raw.Changes {
		files[i] = shared.GitLabMRFile{
			OldPath:     ch.OldPath,
			NewPath:     ch.NewPath,
			NewFile:     ch.NewFile,
			DeletedFile: ch.DeletedFile,
			RenamedFile: ch.RenamedFile,
		}
	}
	return files, nil
}

func (c *Client) GetMRDiscussions(ctx context.Context, projectID, mrIID int64) ([]shared.GitLabDiscussion, error) {
	var all []shared.GitLabDiscussion
	page := 1
	for {
		path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions?page=%d&per_page=100", projectID, mrIID, page)
		data, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		var batch []gitlabDiscussionJSON
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("unmarshal discussions: %w", err)
		}
		if len(batch) == 0 {
			break
		}

		for _, d := range batch {
			all = append(all, d.toDomain())
		}
		page++
	}
	return all, nil
}

func (c *Client) GetDiscussion(ctx context.Context, projectID, mrIID int64, discussionID string) (*shared.GitLabDiscussion, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s", projectID, mrIID, discussionID)
	data, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var raw gitlabDiscussionJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal discussion: %w", err)
	}

	d := raw.toDomain()
	return &d, nil
}

func (c *Client) PostInlineComment(ctx context.Context, req shared.PostInlineCommentRequest) (*shared.PostCommentResponse, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions", req.ProjectID, req.MrIID)
	body := map[string]any{
		"body": req.Body,
		"position": map[string]any{
			"base_sha":      req.BaseSHA,
			"head_sha":      req.HeadSHA,
			"start_sha":     req.StartSHA,
			"position_type": "text",
			"new_path":      req.FilePath,
			"new_line":      req.NewLine,
		},
	}

	data, _, err := c.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}

	var resp struct {
		ID    string `json:"id"`
		Notes []struct {
			ID int64 `json:"id"`
		} `json:"notes"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal post response: %w", err)
	}

	var noteID int64
	if len(resp.Notes) > 0 {
		noteID = resp.Notes[0].ID
	}

	return &shared.PostCommentResponse{
		NoteID:       noteID,
		DiscussionID: resp.ID,
	}, nil
}

func (c *Client) PostThreadComment(ctx context.Context, projectID, mrIID int64, body string) (*shared.PostCommentResponse, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/notes", projectID, mrIID)
	reqBody := map[string]any{"body": body}

	data, _, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return nil, err
	}

	var resp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal note response: %w", err)
	}

	return &shared.PostCommentResponse{NoteID: resp.ID}, nil
}

func (c *Client) PostReply(ctx context.Context, projectID, mrIID int64, discussionID string, body string) (*shared.PostCommentResponse, error) {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s/notes", projectID, mrIID, discussionID)
	reqBody := map[string]any{"body": body}

	data, _, err := c.doRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return nil, err
	}

	var resp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal reply response: %w", err)
	}

	return &shared.PostCommentResponse{
		NoteID:       resp.ID,
		DiscussionID: discussionID,
	}, nil
}

func (c *Client) ResolveDiscussion(ctx context.Context, projectID, mrIID int64, discussionID string) error {
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s", projectID, mrIID, discussionID)
	body := map[string]any{"resolved": true}
	_, _, err := c.doRequest(ctx, http.MethodPut, path, body)
	return err
}

func (c *Client) GetProject(ctx context.Context, projectID int64) (*shared.GitLabProject, error) {
	path := fmt.Sprintf("/projects/%d", projectID)
	data, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var raw struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal project: %w", err)
	}

	return &shared.GitLabProject{
		ID:         raw.ID,
		PathWithNS: raw.PathWithNamespace,
	}, nil
}

// ProjectBasic holds minimal project info needed for bulk clone operations.
type ProjectBasic struct {
	ID                int64
	PathWithNamespace string
	HTTPURLToRepo     string
}

// ListAllProjects fetches every project the current token can access, using
// keyset-based pagination. minAccessLevel filters by access level
// (0 = no filter, 20 = reporter, 30 = developer, 40 = maintainer, 50 = owner).
func (c *Client) ListAllProjects(ctx context.Context, minAccessLevel int) ([]ProjectBasic, error) {
	var all []ProjectBasic
	path := "/projects?membership=true&per_page=100&order_by=id&sort=asc"
	if minAccessLevel > 0 {
		path += fmt.Sprintf("&min_access_level=%d", minAccessLevel)
	}

	for page := 1; ; {
		pagePath := fmt.Sprintf("%s&page=%d", path, page)
		data, headers, err := c.doRequest(ctx, http.MethodGet, pagePath, nil)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}

		var batch []struct {
			ID                int64  `json:"id"`
			PathWithNamespace string `json:"path_with_namespace"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
		}
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("unmarshal projects page %d: %w", page, err)
		}
		for _, p := range batch {
			all = append(all, ProjectBasic{
				ID:                p.ID,
				PathWithNamespace: p.PathWithNamespace,
				HTTPURLToRepo:     p.HTTPURLToRepo,
			})
		}

		// GitLab sets X-Next-Page to "" on the last page.
		next := headers.Get("X-Next-Page")
		if next == "" || next == "0" {
			break
		}
		nextPage, err := strconv.Atoi(next)
		if err != nil || nextPage <= page {
			break
		}
		page = nextPage
	}
	return all, nil
}

// ─── JSON helpers ────────────────────────────────────────────────────────────────

type gitlabDiscussionJSON struct {
	ID    string `json:"id"`
	Notes []struct {
		ID     int64 `json:"id"`
		Author struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"author"`
		Body       string `json:"body"`
		Resolvable bool   `json:"resolvable"`
		Resolved   bool   `json:"resolved"`
		Position   *struct {
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
			OldLine int    `json:"old_line"`
		} `json:"position"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"notes"`
}

func (d gitlabDiscussionJSON) toDomain() shared.GitLabDiscussion {
	disc := shared.GitLabDiscussion{ID: d.ID}
	for _, n := range d.Notes {
		note := shared.GitLabNote{
			ID:         n.ID,
			AuthorID:   n.Author.ID,
			AuthorName: n.Author.Username,
			Body:       n.Body,
			Resolvable: n.Resolvable,
			Resolved:   n.Resolved,
			CreatedAt:  n.CreatedAt,
		}
		if n.Position != nil {
			note.Position = &shared.GitLabNotePosition{
				FilePath: n.Position.NewPath,
				NewLine:  n.Position.NewLine,
				OldLine:  n.Position.OldLine,
			}
		}
		disc.Notes = append(disc.Notes, note)
	}
	return disc
}
