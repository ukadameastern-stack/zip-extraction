package classification

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew_EmptyURL_ReturnsDisabled(t *testing.T) {
	c := New(Config{URL: ""})
	out, err := c.Classify(context.Background(), Request{WorkspaceID: "ws", Body: strings.NewReader("x")})
	if err != nil {
		t.Fatalf("disabled classifier should never error: %v", err)
	}
	if out != nil {
		t.Fatalf("disabled classifier should return nil result, got %+v", out)
	}
}

func TestNew_WhitespaceURL_ReturnsDisabled(t *testing.T) {
	c := New(Config{URL: "   "})
	if _, ok := c.(disabled); !ok {
		t.Fatalf("whitespace-only URL should produce disabled classifier, got %T", c)
	}
}

func TestClient_Classify_HappyPath(t *testing.T) {
	var (
		gotWorkspaceID  string
		gotExtension    string
		gotContentType  string
		gotDepth        string
		gotFileName     string
		gotFileContents []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/classify" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content-type: %v / %q", err, r.Header.Get("Content-Type"))
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			body, _ := io.ReadAll(part)
			switch part.FormName() {
			case "file":
				gotFileName = part.FileName()
				gotFileContents = body
			case "workspaceId":
				gotWorkspaceID = string(body)
			case "extension":
				gotExtension = string(body)
			case "contentType":
				gotContentType = string(body)
			case "parentArchiveDepth":
				gotDepth = string(body)
			}
		}

		_, _ = w.Write([]byte(`{
			"ok": true,
			"elapsedMs": 42,
			"documentId": "doc-x",
			"objectKey": "ui/doc-x/hello.pdf",
			"inputName": "hello.pdf",
			"result": {
				"documentId": "doc-x",
				"workspaceId": "wks-ui-001",
				"classification": {
					"format": "pdf",
					"category": "ocr-direct",
					"subCategory": null,
					"confidenceScore": 0.91,
					"detectionTier": "file-type",
					"isForcedSlipsheet": false,
					"slipsheetReason": null
				},
				"dedup": { "contentHash": "abc123", "isDuplicate": false },
				"policyVersion": "v1"
			}
		}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Timeout: 5 * time.Second})

	res, err := c.Classify(context.Background(), Request{
		WorkspaceID:        "wks-ui-001",
		Filename:           "input/exec-1/0001-hello.pdf",
		ContentType:        "application/pdf",
		ParentArchiveDepth: 1,
		Body:               bytes.NewReader([]byte("PDF-bytes")),
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if res.Format != "pdf" || res.Category != "ocr-direct" {
		t.Errorf("classification fields wrong: %+v", res)
	}
	if res.ConfidenceScore != 0.91 {
		t.Errorf("confidence: %v", res.ConfidenceScore)
	}
	if res.ContentHash != "abc123" {
		t.Errorf("content hash: %v", res.ContentHash)
	}
	if res.PolicyVersion != "v1" {
		t.Errorf("policy version: %v", res.PolicyVersion)
	}
	if res.ElapsedMs != 42 {
		t.Errorf("elapsed: %v", res.ElapsedMs)
	}

	if gotWorkspaceID != "wks-ui-001" {
		t.Errorf("workspaceId form field: %q", gotWorkspaceID)
	}
	if gotExtension != "pdf" {
		t.Errorf("extension form field: %q (want pdf)", gotExtension)
	}
	if gotContentType != "application/pdf" {
		t.Errorf("contentType form field: %q", gotContentType)
	}
	if gotDepth != "1" {
		t.Errorf("parentArchiveDepth form field: %q", gotDepth)
	}
	if gotFileName != "0001-hello.pdf" {
		t.Errorf("filename should be basename, got %q", gotFileName)
	}
	if string(gotFileContents) != "PDF-bytes" {
		t.Errorf("file body lost: %q", gotFileContents)
	}
}

func TestClient_Classify_5xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":"boom"}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	_, err := c.Classify(context.Background(), Request{
		WorkspaceID: "ws", Body: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "http 500") {
		t.Errorf("error should mention http 500: %v", err)
	}
}

func TestClient_Classify_4xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("missing workspaceId"))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	_, err := c.Classify(context.Background(), Request{
		WorkspaceID: "ws", Body: strings.NewReader("x"),
	})
	if err == nil || !strings.Contains(err.Error(), "http 400") {
		t.Fatalf("expected http 400 error, got %v", err)
	}
}

func TestClient_Classify_OkFalse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "elapsedMs": 1}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	_, err := c.Classify(context.Background(), Request{
		WorkspaceID: "ws", Body: strings.NewReader("x"),
	})
	if err == nil || !strings.Contains(err.Error(), "ok=false") {
		t.Fatalf("expected ok=false error, got %v", err)
	}
}

func TestClient_Classify_MalformedJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL})
	_, err := c.Classify(context.Background(), Request{
		WorkspaceID: "ws", Body: strings.NewReader("x"),
	})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestClient_Classify_MissingWorkspaceID(t *testing.T) {
	c := New(Config{URL: "http://example.invalid"})
	_, err := c.Classify(context.Background(), Request{Body: strings.NewReader("x")})
	if err == nil || !strings.Contains(err.Error(), "workspaceId") {
		t.Fatalf("expected workspaceId error, got %v", err)
	}
}

func TestClient_Classify_MissingBody(t *testing.T) {
	c := New(Config{URL: "http://example.invalid"})
	_, err := c.Classify(context.Background(), Request{WorkspaceID: "ws"})
	if err == nil || !strings.Contains(err.Error(), "body required") {
		t.Fatalf("expected body required, got %v", err)
	}
}

func TestClient_TrailingSlashTrimmed(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true,"result":{"classification":{},"dedup":{}}}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/"})
	_, err := c.Classify(context.Background(), Request{
		WorkspaceID: "ws", Body: strings.NewReader("x"),
	})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if gotPath != "/api/classify" {
		t.Errorf("path should not have double slash, got %q", gotPath)
	}
}
