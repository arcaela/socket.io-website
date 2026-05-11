package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// =============================================================================
// Gemini-specific wire constants. These live here (not in internal/config)
// because they are exclusively the Gemini Code Assist for-individuals shape.
// =============================================================================

const (
	caEndpoint  = "https://cloudcode-pa.googleapis.com"
	caVersion   = "v1internal"
	userinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
)

// clientMetadata is sent on every Code Assist request body. Required by the
// backend to identify which IDE / plugin variant is calling.
var clientMetadata = map[string]any{
	"ideType":    "IDE_UNSPECIFIED",
	"platform":   "PLATFORM_UNSPECIFIED",
	"pluginType": "GEMINI",
}

// ----- Types -----

type Tier struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	Description              string `json:"description,omitempty"`
	IsDefault                bool   `json:"isDefault,omitempty"`
	HasOnboardedPreviously   bool   `json:"hasOnboardedPreviously,omitempty"`
	UpgradeSubscriptionURI   string `json:"upgradeSubscriptionUri,omitempty"`
	UpgradeSubscriptionText  string `json:"upgradeSubscriptionText,omitempty"`
}

type IneligibleTier struct {
	TierID        string `json:"tierId"`
	ReasonCode    string `json:"reasonCode"`
	ReasonMessage string `json:"reasonMessage"`
}

type LoadResponse struct {
	CloudaicompanionProject string           `json:"cloudaicompanionProject,omitempty"`
	CurrentTier             *Tier            `json:"currentTier,omitempty"`
	PaidTier                *Tier            `json:"paidTier,omitempty"`
	AllowedTiers            []Tier           `json:"allowedTiers,omitempty"`
	IneligibleTiers         []IneligibleTier `json:"ineligibleTiers,omitempty"`
	Raw                     map[string]any   `json:"-"`
}

type Operation struct {
	Name     string         `json:"name,omitempty"`
	Done     bool           `json:"done"`
	Response map[string]any `json:"response,omitempty"`
	Error    map[string]any `json:"error,omitempty"`
}

type UserInfo struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Generate request/response

type Part struct {
	Text             string                 `json:"text,omitempty"`
	FunctionCall     map[string]any         `json:"functionCall,omitempty"`
	FunctionResponse map[string]any         `json:"functionResponse,omitempty"`
	Thought          bool                   `json:"thought,omitempty"`
	Extra            map[string]any         `json:"-"`
}

type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

type Candidate struct {
	Content      Content        `json:"content"`
	FinishReason string         `json:"finishReason,omitempty"`
	Index        int            `json:"index,omitempty"`
}

type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount      int `json:"totalTokenCount,omitempty"`
}

type GenerateResponseInner struct {
	Candidates    []Candidate    `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
}

type GenerateResponse struct {
	Response *GenerateResponseInner `json:"response,omitempty"`
	TraceID  string                 `json:"traceId,omitempty"`
}

type GenerationConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	MaxTokens   *int     `json:"maxOutputTokens,omitempty"`
}

type generateInner struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	Tools             []map[string]any  `json:"tools,omitempty"`
}

type generateRequest struct {
	Model        string         `json:"model"`
	Project      string         `json:"project,omitempty"`
	UserPromptID string         `json:"user_prompt_id,omitempty"`
	Request      generateInner  `json:"request"`
}

// ----- Client -----

type Client struct {
	HTTP        *http.Client
	AccessToken string
}

func New(accessToken string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 0}, // no timeout: streaming
		AccessToken: accessToken,
	}
}

func (c *Client) baseURL() string  { return caEndpoint + "/" + caVersion }
func (c *Client) methodURL(m string) string { return c.baseURL() + ":" + m }
func (c *Client) opURL(name string) string  { return c.baseURL() + "/" + name }

func (c *Client) doJSON(ctx context.Context, method, urlStr string, body any) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	build := func() (*http.Request, error) {
		var rdr io.Reader
		if bodyBytes != nil {
			rdr = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
	res, err := c.doWithRetry(ctx, build)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// ----- User info (oauth2 endpoint) -----

func (c *Client) UserInfo(ctx context.Context) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", userinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("userinfo %d: %s", res.StatusCode, string(raw))
	}
	var ui UserInfo
	if err := json.Unmarshal(raw, &ui); err != nil {
		return nil, err
	}
	return &ui, nil
}

// ----- Code Assist methods -----

func (c *Client) LoadCodeAssist(ctx context.Context, projectID string) (*LoadResponse, error) {
	body := map[string]any{
		"cloudaicompanionProject": nilIfEmpty(projectID),
		"metadata":                metadataWithProject(projectID),
	}
	raw, err := c.doJSON(ctx, "POST", c.methodURL("loadCodeAssist"), body)
	if err != nil {
		return nil, err
	}
	var lr LoadResponse
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, err
	}
	// also keep the raw map for diagnostic dumps
	_ = json.Unmarshal(raw, &lr.Raw)
	return &lr, nil
}

func (c *Client) OnboardUser(ctx context.Context, tierID, projectID string) (*Operation, error) {
	body := map[string]any{
		"tierId":   tierID,
		"metadata": metadataWithProject(projectID),
	}
	if !isFreeTier(tierID) {
		body["cloudaicompanionProject"] = nilIfEmpty(projectID)
	}
	raw, err := c.doJSON(ctx, "POST", c.methodURL("onboardUser"), body)
	if err != nil {
		return nil, err
	}
	var op Operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (c *Client) GetOperation(ctx context.Context, name string) (*Operation, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.opURL(name), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("getOperation %d: %s", res.StatusCode, string(raw))
	}
	var op Operation
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (c *Client) WaitForOperation(ctx context.Context, name string, timeout time.Duration) (*Operation, error) {
	deadline := time.Now().Add(timeout)
	delay := 1 * time.Second
	for {
		op, err := c.GetOperation(ctx, name)
		if err != nil {
			return nil, err
		}
		if op.Done {
			return op, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("operation %s timed out", name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay = delay + delay/2
		}
	}
}

// EnsureOnboarded looks up the user's tier and onboards if missing. Returns
// the project id assigned by Google and the active tier id.
func (c *Client) EnsureOnboarded(ctx context.Context) (projectID, tierID string, err error) {
	load, err := c.LoadCodeAssist(ctx, "")
	if err != nil {
		return "", "", err
	}
	if load.CurrentTier != nil && load.CloudaicompanionProject != "" {
		tID := load.CurrentTier.ID
		if load.PaidTier != nil && load.PaidTier.ID != "" {
			tID = load.PaidTier.ID
		}
		return load.CloudaicompanionProject, tID, nil
	}
	tier := pickTier(load)
	op, err := c.OnboardUser(ctx, tier.ID, "")
	if err != nil {
		return "", "", err
	}
	if !op.Done && op.Name != "" {
		if _, err := c.WaitForOperation(ctx, op.Name, 60*time.Second); err != nil {
			return "", "", err
		}
	}
	load2, err := c.LoadCodeAssist(ctx, "")
	if err != nil {
		return "", "", err
	}
	tID := tier.ID
	if load2.CurrentTier != nil && load2.CurrentTier.ID != "" {
		tID = load2.CurrentTier.ID
	}
	if load2.PaidTier != nil && load2.PaidTier.ID != "" {
		tID = load2.PaidTier.ID
	}
	return load2.CloudaicompanionProject, tID, nil
}

// GenerateContent: non-streaming.
func (c *Client) GenerateContent(ctx context.Context, model, projectID, promptID string, contents []Content, systemInstruction *Content, tools []map[string]any) (*GenerateResponse, error) {
	req := generateRequest{
		Model:        model,
		Project:      projectID,
		UserPromptID: promptID,
		Request: generateInner{
			Contents:          contents,
			SystemInstruction: systemInstruction,
		},
	}
	if len(tools) > 0 {
		req.Request.Tools = []map[string]any{
			{"functionDeclarations": tools},
		}
	}
	raw, err := c.doJSON(ctx, "POST", c.methodURL("generateContent"), req)
	if err != nil {
		return nil, err
	}
	var gr GenerateResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, err
	}
	return &gr, nil
}

// PartCallback receives every part the model emits during streaming. The
// caller decides how to translate Text / Thought / FunctionCall.
type PartCallback func(Part)

// StreamGenerateContent: streaming via SSE. Calls cb for each emitted part.
// `tools` is a list of function declarations (already in Gemini schema shape).
// `systemInstruction` is optional; pass nil if no system prompt.
func (c *Client) StreamGenerateContent(
	ctx context.Context,
	model, projectID, promptID string,
	contents []Content,
	systemInstruction *Content,
	tools []map[string]any,
	cb PartCallback,
) (*GenerateResponse, error) {
	req := generateRequest{
		Model:        model,
		Project:      projectID,
		UserPromptID: promptID,
		Request: generateInner{
			Contents:          contents,
			SystemInstruction: systemInstruction,
		},
	}
	if len(tools) > 0 {
		req.Request.Tools = []map[string]any{
			{"functionDeclarations": tools},
		}
	}
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	urlStr := c.methodURL("streamGenerateContent") + "?alt=sse"
	build := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.AccessToken)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}
	res, err := c.doWithRetry(ctx, build)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var dataBuf []string
	final := &GenerateResponse{Response: &GenerateResponseInner{}}

	flush := func() error {
		if len(dataBuf) == 0 {
			return nil
		}
		chunk := strings.Join(dataBuf, "\n")
		dataBuf = dataBuf[:0]
		var partial GenerateResponse
		if err := json.Unmarshal([]byte(chunk), &partial); err != nil {
			return nil // skip malformed chunks
		}
		if partial.Response == nil {
			return nil
		}
		// Emit every part to the callback.
		for _, cand := range partial.Response.Candidates {
			for _, part := range cand.Content.Parts {
				if cb != nil {
					cb(part)
				}
			}
		}
		// Merge metadata (last write wins).
		if partial.Response.UsageMetadata != nil {
			final.Response.UsageMetadata = partial.Response.UsageMetadata
		}
		if partial.Response.ModelVersion != "" {
			final.Response.ModelVersion = partial.Response.ModelVersion
		}
		final.Response.Candidates = append(final.Response.Candidates, partial.Response.Candidates...)
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data: "):
			dataBuf = append(dataBuf, strings.TrimSpace(line[6:]))
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return final, nil
}

// ----- helpers -----

func pickTier(load *LoadResponse) Tier {
	if load.CurrentTier != nil {
		return *load.CurrentTier
	}
	for _, t := range load.AllowedTiers {
		if t.IsDefault {
			return t
		}
	}
	if len(load.AllowedTiers) > 0 {
		return load.AllowedTiers[0]
	}
	return Tier{ID: "free-tier"}
}

func isFreeTier(id string) bool {
	id = strings.ToLower(id)
	return id == "free-tier" || id == "free"
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func metadataWithProject(projectID string) map[string]any {
	m := map[string]any{}
	for k, v := range clientMetadata {
		m[k] = v
	}
	if projectID != "" {
		m["duetProject"] = projectID
	}
	return m
}
