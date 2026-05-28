// Command harness is a developer-only local UI for exercising the
// zip-extraction service against LocalStack.
//
//   - Serves a single-page UI at GET /
//   - Accepts a ZIP upload at POST /api/submit, places it in the LocalStack
//     source bucket, and sends a claim-check message to the SQS queue.
//   - Polls DynamoDB + slipsheet at GET /api/result?execId=...
//   - Proxies the service's /metrics for the live-deltas panel.
//
// This is NOT part of the production service. It's a `test/harness/`
// development utility, distinct from the application code under
// `cmd/zip-extraction/` and `internal/`.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

//go:embed index.html
var indexHTML []byte

type config struct {
	listenAddr        string
	endpointURL       string
	region            string
	queueURL          string
	dlqURL            string
	sourceBucket      string
	stagingBucket     string
	dynamoTable       string
	serviceMetricsURL string
	maxArchiveBytes   int64 // mirrors service bombDefence.maxCompressedSizeBytes — surfaced in UI only
}

func main() {
	var c config
	flag.StringVar(&c.listenAddr, "listen", ":9000", "address for the harness HTTP server")
	flag.StringVar(&c.endpointURL, "endpoint-url", "http://localhost:4566", "AWS SDK endpoint override (LocalStack)")
	flag.StringVar(&c.region, "region", "eu-west-1", "AWS region")
	flag.StringVar(&c.queueURL, "queue-url", "http://localhost:4566/000000000000/zip-extraction-queue", "SQS main queue URL")
	flag.StringVar(&c.dlqURL, "dlq-url", "http://localhost:4566/000000000000/zip-extraction-dlq", "SQS dead-letter queue URL")
	flag.StringVar(&c.sourceBucket, "source-bucket", "doc-uploader-uploads-local", "S3 source bucket where ZIPs are uploaded")
	flag.StringVar(&c.stagingBucket, "staging-bucket", "doc-uploader-staging-local", "S3 staging bucket")
	flag.StringVar(&c.dynamoTable, "dynamo-table", "pipeline_files", "DynamoDB table")
	flag.StringVar(&c.serviceMetricsURL, "service-metrics-url", "http://localhost:8080/metrics", "zip-extraction service /metrics endpoint")
	flag.Int64Var(&c.maxArchiveBytes, "max-archive-bytes", 5368709120, "max source ZIP size accepted by the service in this environment (mirrors bombDefence.maxCompressedSizeBytes; default 5 GiB matches chart/values.yaml). Surfaced in the Submit form as a hint; the service enforces the actual limit.")
	flag.Parse()

	ctx := context.Background()
	// LocalStack accepts any access key; real AWS needs IRSA/env creds from the
	// default chain. Pick by whether an explicit endpoint URL is set.
	cfgOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.region)}
	if c.endpointURL != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithCredentialsProvider(staticCreds{}))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}

	// Empty endpoint-url means "use the default AWS endpoint" (real AWS in
	// DEV05/prod). Only override when explicitly set (typically LocalStack).
	s3Client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		if c.endpointURL != "" {
			o.BaseEndpoint = aws.String(c.endpointURL)
			o.UsePathStyle = true
		}
	})
	sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		if c.endpointURL != "" {
			o.BaseEndpoint = aws.String(c.endpointURL)
		}
	})
	ddbClient := awsddb.NewFromConfig(awsCfg, func(o *awsddb.Options) {
		if c.endpointURL != "" {
			o.BaseEndpoint = aws.String(c.endpointURL)
		}
	})

	srv := &server{cfg: c, s3: s3Client, sqs: sqsClient, ddb: ddbClient, httpc: &http.Client{Timeout: 5 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/submit", srv.handleSubmit)
	mux.HandleFunc("/api/result", srv.handleResult)
	mux.HandleFunc("/api/metrics", srv.handleMetrics)
	mux.HandleFunc("/api/config", srv.handleConfig)
	mux.HandleFunc("/api/queue", srv.handleQueue)
	mux.HandleFunc("/api/runs", srv.handleRuns)
	mux.HandleFunc("/api/clear-runs", srv.handleClearRuns)

	log.Printf("zip-extraction harness listening on %s", c.listenAddr)
	log.Printf("  endpoint:        %s", c.endpointURL)
	log.Printf("  source bucket:   %s", c.sourceBucket)
	log.Printf("  staging bucket:  %s", c.stagingBucket)
	log.Printf("  queue:           %s", c.queueURL)
	log.Printf("  dlq:             %s", c.dlqURL)
	log.Printf("  dynamodb table:  %s", c.dynamoTable)
	log.Printf("  service metrics: %s", c.serviceMetricsURL)
	log.Printf("  max archive:     %d bytes (UI hint only — service enforces)", c.maxArchiveBytes)
	if err := http.ListenAndServe(c.listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

type staticCreds struct{}

func (staticCreds) Retrieve(ctx context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		Source:          "harness-static",
	}, nil
}

type server struct {
	cfg   config
	s3    *awss3.Client
	sqs   *awssqs.Client
	ddb   *awsddb.Client
	httpc *http.Client
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"endpointUrl":     s.cfg.endpointURL,
		"region":          s.cfg.region,
		"queueUrl":        s.cfg.queueURL,
		"sourceBucket":    s.cfg.sourceBucket,
		"stagingBucket":   s.cfg.stagingBucket,
		"dynamoTable":     s.cfg.dynamoTable,
		"maxArchiveBytes": s.cfg.maxArchiveBytes,
	}
	writeJSON(w, http.StatusOK, resp)
}

type submitResp struct {
	PipelineExecutionID string `json:"pipelineExecutionId"`
	SourceKey           string `json:"sourceKey"`
	MessageID           string `json:"sqsMessageId"`
}

func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("archive")
	if err != nil {
		http.Error(w, "archive file required (form field: archive)", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Default tenantId matches the workspace configured in the local
	// classification-service demo UI (the only one its DynamoDB seeds), so a
	// bare Submit click routes the per-child classification hop to a workspace
	// that exists. ClaimCheck.tenantId is a required field (FR-1.2) — leaving
	// it blank would be rejected by the service's SQS schema validation.
	// Operators on a real environment override this via the form field.
	tenantID := stringDefault(r.FormValue("tenantId"), "wks-ui-001")
	documentID := stringDefault(r.FormValue("documentId"), "doc-"+time.Now().UTC().Format("20060102T150405Z"))
	execID := stringDefault(r.FormValue("pipelineExecutionId"), "exec-"+time.Now().UTC().Format("20060102T150405.000Z"))
	correlationID := stringDefault(r.FormValue("correlationId"), "corr-"+execID)
	execID = sanitiseID(execID)

	sourceKey := "uploads/" + execID + "-" + header.Filename

	// `file` is a multipart.File which implements io.ReadSeeker — stream it
	// straight to S3. Avoids io.ReadAll + string copy + Reader wrap that used
	// to triple peak memory and OOMKill the pod on ~50MB uploads.
	// Stash the submitter-supplied IDs as S3 object metadata so /api/runs can
	// surface them on the past-extractions table without changing the service.
	_, err = s.s3.PutObject(r.Context(), &awss3.PutObjectInput{
		Bucket:        aws.String(s.cfg.sourceBucket),
		Key:           aws.String(sourceKey),
		Body:          file,
		ContentType:   aws.String("application/zip"),
		ContentLength: aws.Int64(header.Size),
		Metadata: map[string]string{
			"tenant-id":      tenantID,
			"document-id":    documentID,
			"correlation-id": correlationID,
		},
	})
	if err != nil {
		http.Error(w, "S3 PutObject: "+err.Error(), http.StatusBadGateway)
		return
	}

	msgBody, _ := json.Marshal(map[string]string{
		"pipelineExecutionId": execID,
		"tenantId":            tenantID,
		"documentId":          documentID,
		"sourceBucket":        s.cfg.sourceBucket,
		"sourceKey":           sourceKey,
		"correlationId":       correlationID,
	})
	sqsOut, err := s.sqs.SendMessage(r.Context(), &awssqs.SendMessageInput{
		QueueUrl:    aws.String(s.cfg.queueURL),
		MessageBody: aws.String(string(msgBody)),
	})
	if err != nil {
		http.Error(w, "SQS SendMessage: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, http.StatusOK, submitResp{
		PipelineExecutionID: execID,
		SourceKey:           sourceKey,
		MessageID:           aws.ToString(sqsOut.MessageId),
	})
}

type resultRow struct {
	EntryIndex    string `json:"entryIndex"`
	Status        string `json:"status"`
	ChildKey      string `json:"childKey,omitempty"`
	MimeType      string `json:"mimeType,omitempty"`
	SizeBytes     string `json:"sizeBytes,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	FailureDetail string `json:"failureDetail,omitempty"`
}

type resultResp struct {
	State     string                 `json:"state"` // "pending" | "complete"
	Slipsheet map[string]interface{} `json:"slipsheet,omitempty"`
	DDBRows   []resultRow            `json:"ddbRows"`
	S3Listing []string               `json:"s3Listing"`
	Error     string                 `json:"error,omitempty"`
}

func (s *server) handleResult(w http.ResponseWriter, r *http.Request) {
	execID := r.URL.Query().Get("execId")
	if execID == "" {
		http.Error(w, "execId required", http.StatusBadRequest)
		return
	}
	resp := resultResp{State: "pending", DDBRows: []resultRow{}, S3Listing: []string{}}

	// Try to read the slipsheet — its presence indicates a terminal outcome.
	slipObj, err := s.s3.GetObject(r.Context(), &awss3.GetObjectInput{
		Bucket: aws.String(s.cfg.stagingBucket),
		Key:    aws.String("slipsheets/" + execID + ".json"),
	})
	if err == nil {
		bytes, _ := io.ReadAll(slipObj.Body)
		slipObj.Body.Close()
		var slip map[string]interface{}
		if jerr := json.Unmarshal(bytes, &slip); jerr == nil {
			resp.Slipsheet = slip
			resp.State = "complete"
		}
	}

	// Always include DDB rows (BR-DDB-002 — every entry produces exactly one row).
	pk := "PIPELINE#" + execID
	ddbOut, err := s.ddb.Query(r.Context(), &awsddb.QueryInput{
		TableName:              aws.String(s.cfg.dynamoTable),
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pk": &ddbtypes.AttributeValueMemberS{Value: pk},
		},
	})
	if err == nil {
		for _, item := range ddbOut.Items {
			row := resultRow{
				EntryIndex:    attrS(item, "sk"),
				Status:        attrS(item, "status"),
				ChildKey:      attrS(item, "childKey"),
				MimeType:      attrS(item, "mimeType"),
				SizeBytes:     attrN(item, "sizeBytes"),
				FailureReason: attrS(item, "failureReason"),
				FailureDetail: attrS(item, "failureDetail"),
			}
			resp.DDBRows = append(resp.DDBRows, row)
		}
	} else {
		resp.Error = "ddb: " + err.Error()
	}

	// S3 listing under the canonical input/ prefix for this exec.
	prefix := "input/" + execID + "/"
	listOut, lerr := s.s3.ListObjectsV2(r.Context(), &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.stagingBucket),
		Prefix: aws.String(prefix),
	})
	if lerr == nil {
		for _, o := range listOut.Contents {
			key := aws.ToString(o.Key)
			size := int64(0)
			if o.Size != nil {
				size = *o.Size
			}
			resp.S3Listing = append(resp.S3Listing, fmt.Sprintf("%s (%d bytes)", key, size))
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.serviceMetricsURL, nil)
	resp, err := s.httpc.Do(req)
	if err != nil {
		http.Error(w, "service /metrics unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Filter to the metrics our service emits.
	lines := strings.Split(string(body), "\n")
	keep := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "zip_") ||
			strings.HasPrefix(l, "extracted_") ||
			strings.HasPrefix(l, "partial_failures") ||
			strings.HasPrefix(l, "redelivery_skips") ||
			strings.HasPrefix(l, "slipsheet_write_failures") {
			keep = append(keep, l)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.Join(keep, "\n")))
}

type queueDepth struct {
	Visible  int `json:"visible"`
	InFlight int `json:"inFlight"`
	Delayed  int `json:"delayed"`
}

type queueResp struct {
	Main  queueDepth `json:"main"`
	DLQ   queueDepth `json:"dlq"`
	Error string     `json:"error,omitempty"`
}

func (s *server) handleQueue(w http.ResponseWriter, r *http.Request) {
	resp := queueResp{}
	resp.Main = s.queueDepthFor(r.Context(), s.cfg.queueURL)
	resp.DLQ = s.queueDepthFor(r.Context(), s.cfg.dlqURL)
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) queueDepthFor(ctx context.Context, url string) queueDepth {
	out, err := s.sqs.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed,
		},
	})
	if err != nil {
		return queueDepth{}
	}
	parse := func(k string) int {
		v, ok := out.Attributes[k]
		if !ok {
			return 0
		}
		n, _ := strconv.Atoi(v)
		return n
	}
	return queueDepth{
		Visible:  parse("ApproximateNumberOfMessages"),
		InFlight: parse("ApproximateNumberOfMessagesNotVisible"),
		Delayed:  parse("ApproximateNumberOfMessagesDelayed"),
	}
}

type runSummary struct {
	ExecID                string `json:"execId"`
	SourceArchive         string `json:"sourceArchive"`
	TenantID              string `json:"tenantId,omitempty"`
	SourceSizeBytes       int64  `json:"sourceSizeBytes,omitempty"`
	ChildCount            int    `json:"childCount"`
	Status                string `json:"status"`
	WrittenAt             string `json:"writtenAt"`
	FailureReason         string `json:"failureReason,omitempty"`
	SlipsheetKey          string `json:"slipsheetKey"`
	ClassificationSummary string `json:"classificationSummary,omitempty"` // e.g. "convert:2, ocr-direct:1"
	ClassifiedCount       int    `json:"classifiedCount"`
}

type runsResp struct {
	Runs  []runSummary `json:"runs"`
	Error string       `json:"error,omitempty"`
}

func (s *server) handleRuns(w http.ResponseWriter, r *http.Request) {
	listOut, err := s.s3.ListObjectsV2(r.Context(), &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.stagingBucket),
		Prefix: aws.String("slipsheets/"),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, runsResp{Runs: []runSummary{}, Error: err.Error()})
		return
	}

	// Most recent first.
	sort.Slice(listOut.Contents, func(i, j int) bool {
		ti := listOut.Contents[i].LastModified
		tj := listOut.Contents[j].LastModified
		if ti == nil || tj == nil {
			return false
		}
		return ti.After(*tj)
	})

	const maxRuns = 50
	runs := make([]runSummary, 0, len(listOut.Contents))
	for i, obj := range listOut.Contents {
		if i >= maxRuns {
			break
		}
		key := aws.ToString(obj.Key)
		execID := strings.TrimSuffix(strings.TrimPrefix(key, "slipsheets/"), ".json")

		summary := runSummary{ExecID: execID, SlipsheetKey: key}

		getOut, gerr := s.s3.GetObject(r.Context(), &awss3.GetObjectInput{
			Bucket: aws.String(s.cfg.stagingBucket),
			Key:    aws.String(key),
		})
		if gerr == nil {
			body, _ := io.ReadAll(getOut.Body)
			getOut.Body.Close()
			var slip map[string]interface{}
			if jerr := json.Unmarshal(body, &slip); jerr == nil {
				summary.SourceArchive, _ = slip["sourceArchive"].(string)
				summary.Status, _ = slip["status"].(string)
				summary.WrittenAt, _ = slip["writtenAt"].(string)
				summary.FailureReason, _ = slip["failureReason"].(string)
				if cc, ok := slip["childCount"].(float64); ok {
					summary.ChildCount = int(cc)
				}
				summary.ClassificationSummary, summary.ClassifiedCount = classificationSummary(slip)
			}
		}

		// HEAD the source archive to grab the upload's size + the tenantId we
		// stashed on PutObject metadata. Best-effort: a missing source object
		// (rare — e.g. lifecycle expiry) just leaves these fields empty.
		if summary.SourceArchive != "" {
			headOut, herr := s.s3.HeadObject(r.Context(), &awss3.HeadObjectInput{
				Bucket: aws.String(s.cfg.sourceBucket),
				Key:    aws.String(summary.SourceArchive),
			})
			if herr == nil {
				if headOut.ContentLength != nil {
					summary.SourceSizeBytes = *headOut.ContentLength
				}
				// S3 SDK lowercases user-metadata keys.
				summary.TenantID = headOut.Metadata["tenant-id"]
			}
		}
		runs = append(runs, summary)
	}
	writeJSON(w, http.StatusOK, runsResp{Runs: runs})
}

type clearResp struct {
	DeletedStagingObjects int    `json:"deletedStagingObjects"`
	DeletedSourceObjects  int    `json:"deletedSourceObjects"`
	DeletedDdbRows        int    `json:"deletedDdbRows"`
	Error                 string `json:"error,omitempty"`
}

// handleClearRuns wipes every artefact the past-extractions table reads from:
// slipsheets, per-exec child files, source ZIPs, and PIPELINE# DDB rows. There
// is no undo. Dev-only — the harness has no auth.
func (s *server) handleClearRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE required", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	resp := clearResp{}

	for _, p := range []string{"slipsheets/", "input/"} {
		n, err := s.deleteS3Prefix(ctx, s.cfg.stagingBucket, p)
		resp.DeletedStagingObjects += n
		if err != nil {
			resp.Error = "staging " + p + ": " + err.Error()
			writeJSON(w, http.StatusInternalServerError, resp)
			return
		}
	}

	n, err := s.deleteS3Prefix(ctx, s.cfg.sourceBucket, "uploads/")
	resp.DeletedSourceObjects = n
	if err != nil {
		resp.Error = "source uploads/: " + err.Error()
		writeJSON(w, http.StatusInternalServerError, resp)
		return
	}

	n, err = s.deletePipelineDdbRows(ctx)
	resp.DeletedDdbRows = n
	if err != nil {
		resp.Error = "ddb: " + err.Error()
		writeJSON(w, http.StatusInternalServerError, resp)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *server) deleteS3Prefix(ctx context.Context, bucket, prefix string) (int, error) {
	deleted := 0
	var token *string
	for {
		listOut, err := s.s3.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return deleted, err
		}
		if len(listOut.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, 0, len(listOut.Contents))
			for _, o := range listOut.Contents {
				ids = append(ids, s3types.ObjectIdentifier{Key: o.Key})
			}
			if _, err := s.s3.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			}); err != nil {
				return deleted, err
			}
			deleted += len(ids)
		}
		if listOut.IsTruncated == nil || !*listOut.IsTruncated {
			return deleted, nil
		}
		token = listOut.NextContinuationToken
	}
}

func (s *server) deletePipelineDdbRows(ctx context.Context) (int, error) {
	deleted := 0
	var lastKey map[string]ddbtypes.AttributeValue
	for {
		scanOut, err := s.ddb.Scan(ctx, &awsddb.ScanInput{
			TableName:        aws.String(s.cfg.dynamoTable),
			FilterExpression: aws.String("begins_with(pk, :p)"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":p": &ddbtypes.AttributeValueMemberS{Value: "PIPELINE#"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return deleted, err
		}
		for i := 0; i < len(scanOut.Items); i += 25 {
			end := i + 25
			if end > len(scanOut.Items) {
				end = len(scanOut.Items)
			}
			reqs := make([]ddbtypes.WriteRequest, 0, end-i)
			for _, item := range scanOut.Items[i:end] {
				reqs = append(reqs, ddbtypes.WriteRequest{
					DeleteRequest: &ddbtypes.DeleteRequest{
						Key: map[string]ddbtypes.AttributeValue{
							"pk": item["pk"],
							"sk": item["sk"],
						},
					},
				})
			}
			if _, err := s.ddb.BatchWriteItem(ctx, &awsddb.BatchWriteItemInput{
				RequestItems: map[string][]ddbtypes.WriteRequest{s.cfg.dynamoTable: reqs},
			}); err != nil {
				return deleted, err
			}
			deleted += len(reqs)
		}
		if len(scanOut.LastEvaluatedKey) == 0 {
			return deleted, nil
		}
		lastKey = scanOut.LastEvaluatedKey
	}
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// classificationSummary walks slipsheet.children[].classification.category and
// returns a compact "cat:n, cat:n" summary + the total classified count.
// Returns ("", 0) when no entries carry a classification block.
func classificationSummary(slip map[string]interface{}) (summary string, classified int) {
	children, ok := slip["children"].([]interface{})
	if !ok {
		return "", 0
	}
	counts := map[string]int{}
	total := 0
	for _, c := range children {
		row, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cls, ok := row["classification"].(map[string]interface{})
		if !ok {
			continue
		}
		cat, _ := cls["category"].(string)
		if cat == "" {
			cat = "?"
		}
		counts[cat]++
		total++
	}
	if total == 0 {
		return "", 0
	}
	// Stable ordering: alphabetic by category.
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, ", "), total
}

func attrS(item map[string]ddbtypes.AttributeValue, key string) string {
	if v, ok := item[key]; ok {
		if s, ok := v.(*ddbtypes.AttributeValueMemberS); ok {
			return s.Value
		}
	}
	return ""
}

func attrN(item map[string]ddbtypes.AttributeValue, key string) string {
	if v, ok := item[key]; ok {
		if n, ok := v.(*ddbtypes.AttributeValueMemberN); ok {
			return n.Value
		}
	}
	return ""
}

func stringDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// sanitiseID strips characters that would break S3 keys or DDB sort keys.
func sanitiseID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
