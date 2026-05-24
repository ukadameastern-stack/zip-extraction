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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

//go:embed index.html
var indexHTML []byte

type config struct {
	listenAddr      string
	endpointURL     string
	region          string
	queueURL        string
	sourceBucket    string
	stagingBucket   string
	dynamoTable     string
	serviceMetricsURL string
}

func main() {
	var c config
	flag.StringVar(&c.listenAddr, "listen", ":9000", "address for the harness HTTP server")
	flag.StringVar(&c.endpointURL, "endpoint-url", "http://localhost:4566", "AWS SDK endpoint override (LocalStack)")
	flag.StringVar(&c.region, "region", "eu-west-1", "AWS region")
	flag.StringVar(&c.queueURL, "queue-url", "http://localhost:4566/000000000000/zip-extraction-queue", "SQS main queue URL")
	flag.StringVar(&c.sourceBucket, "source-bucket", "doc-uploader-uploads-local", "S3 source bucket where ZIPs are uploaded")
	flag.StringVar(&c.stagingBucket, "staging-bucket", "doc-uploader-staging-local", "S3 staging bucket")
	flag.StringVar(&c.dynamoTable, "dynamo-table", "pipeline_files", "DynamoDB table")
	flag.StringVar(&c.serviceMetricsURL, "service-metrics-url", "http://localhost:8080/metrics", "zip-extraction service /metrics endpoint")
	flag.Parse()

	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(c.region),
		awsconfig.WithCredentialsProvider(staticCreds{}),
	)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}

	s3Client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(c.endpointURL)
		o.UsePathStyle = true
	})
	sqsClient := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(c.endpointURL)
	})
	ddbClient := awsddb.NewFromConfig(awsCfg, func(o *awsddb.Options) {
		o.BaseEndpoint = aws.String(c.endpointURL)
	})

	srv := &server{cfg: c, s3: s3Client, sqs: sqsClient, ddb: ddbClient, httpc: &http.Client{Timeout: 5 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/submit", srv.handleSubmit)
	mux.HandleFunc("/api/result", srv.handleResult)
	mux.HandleFunc("/api/metrics", srv.handleMetrics)
	mux.HandleFunc("/api/config", srv.handleConfig)

	log.Printf("zip-extraction harness listening on %s", c.listenAddr)
	log.Printf("  endpoint:        %s", c.endpointURL)
	log.Printf("  source bucket:   %s", c.sourceBucket)
	log.Printf("  staging bucket:  %s", c.stagingBucket)
	log.Printf("  queue:           %s", c.queueURL)
	log.Printf("  dynamodb table:  %s", c.dynamoTable)
	log.Printf("  service metrics: %s", c.serviceMetricsURL)
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
	resp := map[string]string{
		"endpointUrl":   s.cfg.endpointURL,
		"region":        s.cfg.region,
		"queueUrl":      s.cfg.queueURL,
		"sourceBucket":  s.cfg.sourceBucket,
		"stagingBucket": s.cfg.stagingBucket,
		"dynamoTable":   s.cfg.dynamoTable,
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

	tenantID := stringDefault(r.FormValue("tenantId"), "tenant-harness")
	documentID := stringDefault(r.FormValue("documentId"), "doc-"+time.Now().UTC().Format("20060102T150405Z"))
	execID := stringDefault(r.FormValue("pipelineExecutionId"), "exec-"+time.Now().UTC().Format("20060102T150405.000Z"))
	correlationID := stringDefault(r.FormValue("correlationId"), "corr-"+execID)
	execID = sanitiseID(execID)

	body, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	sourceKey := "uploads/" + execID + "-" + header.Filename

	_, err = s.s3.PutObject(r.Context(), &awss3.PutObjectInput{
		Bucket:      aws.String(s.cfg.sourceBucket),
		Key:         aws.String(sourceKey),
		Body:        strings.NewReader(string(body)),
		ContentType: aws.String("application/zip"),
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
	State      string                 `json:"state"` // "pending" | "complete"
	Slipsheet  map[string]interface{} `json:"slipsheet,omitempty"`
	DDBRows    []resultRow            `json:"ddbRows"`
	S3Listing  []string               `json:"s3Listing"`
	Error      string                 `json:"error,omitempty"`
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

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
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
