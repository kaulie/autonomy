// Package knowledgebase is Autonomy's read client for the independent
// knowledge-base service. Agent context.list / search / get go through here.
// Register, bind and sync stay on knowledge-base; this client refuses writes.
package knowledgebase

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	ctxsvc "github.com/kaulie/autonomy/src/context"
)

const (
	// EnvURL is the knowledge-base base URL. Empty uses DefaultURL.
	EnvURL = "KNOWLEDGE_BASE_URL"
	// DefaultURL is the local knowledge-base the service contract declares.
	DefaultURL = "http://127.0.0.1:4360"
	// EntityProject is the CONTEXT entity type Autonomy's project_id maps onto.
	EntityProject = "project"

	httpTimeout  = 10 * time.Second
	maxBodyBytes = 4 << 20
)

// Client is ctxsvc.Service over knowledge-base HTTP. project_id becomes
// {type:project, id}. Knowledge-base has no project_id field of its own.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New is the client as the runtime wires it.
func New() *Client { return &Client{} }

func (c *Client) BaseURL() string {
	if c != nil && strings.TrimSpace(c.URL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.URL), "/")
	}
	if v := strings.TrimSpace(os.Getenv(EnvURL)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultURL
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: httpTimeout}
}

func maintained() error {
	return ctxsvc.Errorf(ctxsvc.ErrIndexFailed, "knowledge is maintained on knowledge-base")
}

func (c *Client) RegisterResource(ctx context.Context, resource ctxsvc.Resource) error {
	_, err := c.Register(ctx, resource)
	return err
}

func (c *Client) Register(context.Context, ctxsvc.Resource) (ctxsvc.Resource, error) {
	return ctxsvc.Resource{}, maintained()
}

func (c *Client) SyncResource(ctx context.Context, resourceID string) error {
	_, err := c.Sync(ctx, resourceID)
	return err
}

func (c *Client) Sync(context.Context, string) (ctxsvc.SyncResult, error) {
	return ctxsvc.SyncResult{}, maintained()
}

func (c *Client) DeleteResource(context.Context, string) error { return maintained() }

func (c *Client) ListResources(ctx context.Context, projectID string) ([]ctxsvc.Resource, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrProjectNotFound, "list requires a project id")
	}
	var body struct {
		Knowledge []kbKnowledge `json:"knowledge"`
	}
	path := "/api/contexts/" + url.PathEscape(EntityProject) + "/" + url.PathEscape(projectID) + "/knowledge"
	if err := c.getJSON(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	out := make([]ctxsvc.Resource, 0, len(body.Knowledge))
	for _, k := range body.Knowledge {
		out = append(out, knowledgeToResource(k, projectID))
	}
	return out, nil
}

func (c *Client) Search(ctx context.Context, req ctxsvc.SearchRequest) ([]ctxsvc.SearchResult, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrProjectNotFound, "search requires a project id")
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "search query %q has no searchable terms", req.Query)
	}
	q := url.Values{}
	q.Set("q", req.Query)
	if req.Limit > 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	path := "/api/contexts/" + url.PathEscape(EntityProject) + "/" + url.PathEscape(strings.TrimSpace(req.ProjectID)) + "/search"
	var body struct {
		Results []kbSearchResult `json:"results"`
	}
	if err := c.getJSON(ctx, path, q, &body); err != nil {
		return nil, err
	}
	out := make([]ctxsvc.SearchResult, 0, len(body.Results))
	for _, r := range body.Results {
		out = append(out, ctxsvc.SearchResult{
			ResourceID:   r.KnowledgeID,
			SectionID:    r.SectionID,
			ResourceName: r.DocumentName,
			Heading:      r.Heading,
			Snippet:      r.Snippet,
			Score:        r.Score,
			Revision:     r.Revision,
		})
	}
	return out, nil
}

func (c *Client) GetResource(ctx context.Context, resourceID string) (*ctxsvc.Resource, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "resource id is required")
	}
	var k kbKnowledge
	if err := c.getJSON(ctx, "/api/knowledge/"+url.PathEscape(resourceID), nil, &k); err != nil {
		return nil, err
	}
	r := knowledgeToResource(k, "")
	return &r, nil
}

func (c *Client) GetSection(ctx context.Context, sectionID string) (*ctxsvc.Section, error) {
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" {
		return nil, ctxsvc.Errorf(ctxsvc.ErrInvalidQuery, "section id is required")
	}
	var s kbSection
	if err := c.getJSON(ctx, "/api/sections/"+url.PathEscape(sectionID), nil, &s); err != nil {
		return nil, err
	}
	return &ctxsvc.Section{
		ID:         s.ID,
		ResourceID: s.KnowledgeID,
		Heading:    s.Heading,
		Path:       s.Path,
		Content:    s.Content,
		Order:      s.Order,
		StartLine:  s.StartLine,
		EndLine:    s.EndLine,
		Revision:   s.Revision,
		Source: ctxsvc.ResourceSource{
			Location:   s.Source.Location,
			Repository: s.Source.Repository,
			Path:       s.Source.Path,
		},
	}, nil
}

type kbSource struct {
	Location   string `json:"location,omitempty"`
	Path       string `json:"path,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type kbKnowledge struct {
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	Kind      string            `json:"kind"`
	Provider  string            `json:"provider"`
	Source    kbSource          `json:"source"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Revision  string            `json:"revision,omitempty"`
	Checksum  string            `json:"checksum,omitempty"`
	Files     []string          `json:"files,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type kbSearchResult struct {
	KnowledgeID  string  `json:"knowledge_id"`
	SectionID    string  `json:"section_id"`
	DocumentName string  `json:"document_name"`
	Heading      string  `json:"heading"`
	Snippet      string  `json:"snippet"`
	Score        float64 `json:"score"`
	Revision     string  `json:"revision,omitempty"`
}

type kbSection struct {
	ID          string   `json:"id"`
	KnowledgeID string   `json:"knowledge_id"`
	Heading     string   `json:"heading"`
	Path        string   `json:"path,omitempty"`
	Content     string   `json:"content"`
	Order       int      `json:"order"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	Revision    string   `json:"revision,omitempty"`
	Source      kbSource `json:"source,omitempty"`
}

func knowledgeToResource(k kbKnowledge, projectID string) ctxsvc.Resource {
	return ctxsvc.Resource{
		ID:        k.ID,
		ProjectID: projectID,
		Type:      resourceType(k.Kind),
		Name:      k.Name,
		URI:       k.Source.Location,
		Source: ctxsvc.ResourceSource{
			Kind:       k.Kind,
			Provider:   k.Provider,
			Type:       legacySourceType(k.Kind),
			Location:   k.Source.Location,
			Repository: k.Source.Repository,
			Path:       k.Source.Path,
		},
		Metadata:  k.Metadata,
		Revision:  k.Revision,
		Checksum:  k.Checksum,
		Files:     k.Files,
		CreatedAt: k.CreatedAt,
		UpdatedAt: k.UpdatedAt,
	}
}

func resourceType(kind string) ctxsvc.ResourceType {
	switch kind {
	case "source_code_repository":
		return ctxsvc.ResourceRepository
	case "cloud":
		return ctxsvc.ResourceService
	default:
		return ctxsvc.ResourceDocument
	}
}

func legacySourceType(kind string) string {
	switch kind {
	case "source_code_repository":
		return "git"
	case "cloud":
		return "external"
	default:
		return "local_file"
	}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	raw := c.BaseURL() + path
	if len(q) > 0 {
		raw += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "knowledge-base request")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "knowledge-base %s", raw)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "knowledge-base %s", raw)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mapHTTPError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return ctxsvc.Wrap(ctxsvc.ErrIndexFailed, err, "knowledge-base %s: decode", raw)
	}
	return nil
}

func mapHTTPError(status int, body []byte) error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	code := ctxsvc.ErrorCode(e.Code)
	switch code {
	case "NOT_FOUND":
		code = ctxsvc.ErrResourceNotFound
	case ctxsvc.ErrInvalidQuery, ctxsvc.ErrSourceUnavailable, ctxsvc.ErrParseFailed,
		ctxsvc.ErrIndexFailed, ctxsvc.ErrProjectNotFound, ctxsvc.ErrResourceNotFound:
		// keep knowledge-base code when Autonomy already understands it
	default:
		switch status {
		case http.StatusNotFound:
			code = ctxsvc.ErrResourceNotFound
		case http.StatusBadRequest:
			code = ctxsvc.ErrInvalidQuery
		default:
			code = ctxsvc.ErrIndexFailed
		}
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = fmt.Sprintf("knowledge-base http %d", status)
	}
	return ctxsvc.Errorf(code, "%s", msg)
}

var _ ctxsvc.Service = (*Client)(nil)
