// Package classification is the HTTP client for the classification service's
// /api/classify endpoint. After zip-extraction writes a child to the staging
// bucket, the orchestrator (extraction.Service) calls Classifier.Classify so
// the result can be stamped onto the slipsheet for downstream consumers.
//
// Disabled by default: an empty URL produces a Disabled() classifier whose
// Classify is a no-op returning (nil, nil) — keeps zip-extraction's primary
// contract (extract + slipsheet) independent of classification availability.
package classification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Result is the compact, slipsheet-friendly subset of the classification
// response. Fields mirror the relevant parts of the upstream
// `ClassificationOutput` schema (ui/public/openapi.yaml in the classification
// repo) — we deliberately drop the wrapping {ok, elapsedMs, ...} envelope.
type Result struct {
	Format            string  `json:"format"`
	Category          string  `json:"category"`
	SubCategory       string  `json:"subCategory,omitempty"`
	ConfidenceScore   float64 `json:"confidenceScore"`
	DetectionTier     string  `json:"detectionTier"`
	IsForcedSlipsheet bool    `json:"isForcedSlipsheet"`
	SlipsheetReason   string  `json:"slipsheetReason,omitempty"`
	ContentHash       string  `json:"contentHash,omitempty"`
	IsDuplicate       bool    `json:"isDuplicate,omitempty"`
	PolicyVersion     string  `json:"policyVersion,omitempty"`
	ElapsedMs         int     `json:"elapsedMs,omitempty"`
}

// Request carries the per-child inputs to /api/classify.
type Request struct {
	WorkspaceID        string
	Filename           string
	ContentType        string
	ParentArchiveDepth int
	Body               io.Reader
}

// Classifier is the port consumed by extraction.Service.
type Classifier interface {
	Classify(ctx context.Context, req Request) (*Result, error)
}

// Client is the HTTP implementation of Classifier.
type Client struct {
	url     string
	http    *http.Client
	timeout time.Duration
}

// Config holds Client tunables.
type Config struct {
	URL     string        // empty disables classification
	Timeout time.Duration // per-request timeout; defaults to 30s
}

// New constructs a Classifier. When cfg.URL is empty, returns Disabled().
func New(cfg Config) Classifier {
	if strings.TrimSpace(cfg.URL) == "" {
		return Disabled()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		url:     strings.TrimRight(cfg.URL, "/"),
		http:    &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

// Classify uploads req.Body as multipart form-data and parses the response.
// Returns (nil, nil) only when this is the Disabled() classifier; the HTTP
// client always returns either (*Result, nil) or (nil, error).
func (c *Client) Classify(ctx context.Context, req Request) (*Result, error) {
	if req.WorkspaceID == "" {
		return nil, fmt.Errorf("classification: workspaceId required")
	}
	if req.Body == nil {
		return nil, fmt.Errorf("classification: body required")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("file", baseName(req.Filename))
	if err != nil {
		return nil, fmt.Errorf("classification: create form file: %w", err)
	}
	if _, copyErr := io.Copy(fw, req.Body); copyErr != nil {
		return nil, fmt.Errorf("classification: copy body: %w", copyErr)
	}

	if wsErr := mw.WriteField("workspaceId", req.WorkspaceID); wsErr != nil {
		return nil, fmt.Errorf("classification: write workspaceId: %w", wsErr)
	}
	if ext := extensionHint(req.Filename); ext != "" {
		_ = mw.WriteField("extension", ext)
	}
	if req.ContentType != "" {
		_ = mw.WriteField("contentType", req.ContentType)
	}
	if req.ParentArchiveDepth > 0 {
		_ = mw.WriteField("parentArchiveDepth", fmt.Sprintf("%d", req.ParentArchiveDepth))
	}
	if closeErr := mw.Close(); closeErr != nil {
		return nil, fmt.Errorf("classification: close multipart: %w", closeErr)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/api/classify", &buf)
	if err != nil {
		return nil, fmt.Errorf("classification: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("classification: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("classification: http %d: %s", resp.StatusCode, truncate(string(body), 512))
	}

	return parseResponse(resp.Body)
}

// envelope mirrors the /api/classify success body.
type envelope struct {
	OK        bool `json:"ok"`
	ElapsedMs int  `json:"elapsedMs"`
	Result    struct {
		WorkspaceID    string `json:"workspaceId"`
		Classification struct {
			Format            string  `json:"format"`
			Category          string  `json:"category"`
			SubCategory       string  `json:"subCategory"`
			ConfidenceScore   float64 `json:"confidenceScore"`
			DetectionTier     string  `json:"detectionTier"`
			IsForcedSlipsheet bool    `json:"isForcedSlipsheet"`
			SlipsheetReason   string  `json:"slipsheetReason"`
		} `json:"classification"`
		Dedup struct {
			ContentHash string `json:"contentHash"`
			IsDuplicate bool   `json:"isDuplicate"`
		} `json:"dedup"`
		PolicyVersion string `json:"policyVersion"`
	} `json:"result"`
}

func parseResponse(r io.Reader) (*Result, error) {
	var env envelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return nil, fmt.Errorf("classification: decode response: %w", err)
	}
	if !env.OK {
		return nil, fmt.Errorf("classification: response ok=false")
	}
	return &Result{
		Format:            env.Result.Classification.Format,
		Category:          env.Result.Classification.Category,
		SubCategory:       env.Result.Classification.SubCategory,
		ConfidenceScore:   env.Result.Classification.ConfidenceScore,
		DetectionTier:     env.Result.Classification.DetectionTier,
		IsForcedSlipsheet: env.Result.Classification.IsForcedSlipsheet,
		SlipsheetReason:   env.Result.Classification.SlipsheetReason,
		ContentHash:       env.Result.Dedup.ContentHash,
		IsDuplicate:       env.Result.Dedup.IsDuplicate,
		PolicyVersion:     env.Result.PolicyVersion,
		ElapsedMs:         env.ElapsedMs,
	}, nil
}

// disabled is the no-op Classifier returned when Config.URL is empty.
type disabled struct{}

// Disabled returns a Classifier whose Classify is a no-op returning (nil, nil).
// Lets call sites use the Classifier port unconditionally without checking
// for nil — and lets configuration alone (empty URL) flip classification off.
func Disabled() Classifier { return disabled{} }

func (disabled) Classify(context.Context, Request) (*Result, error) { return nil, nil }

func baseName(p string) string {
	if p == "" {
		return "child"
	}
	b := filepath.Base(p)
	if b == "" || b == "." || b == "/" {
		return "child"
	}
	return b
}

func extensionHint(p string) string {
	ext := filepath.Ext(p)
	if ext == "" {
		return ""
	}
	return strings.TrimPrefix(ext, ".")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
