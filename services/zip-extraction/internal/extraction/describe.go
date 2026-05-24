package extraction

import (
	"fmt"
	"strings"
)

// DescribeFailure returns a human-readable explanation for a controlled-
// vocabulary failure reason (BR-DDB-005). If detail is non-empty, it is
// appended as the dynamic-context suffix; otherwise a generic description is
// returned. If reason is unknown, DescribeFailure returns detail (or reason
// when detail is empty) — never an empty string when called with a non-empty
// reason.
//
// This helper is used by the slipsheet writer and the orchestrator to populate
// the `FailureDetail` field next to the machine-readable `FailureReason`
// (Prometheus label cardinality stays bounded because the metric uses the
// reason, not the detail).
func DescribeFailure(reason, detail string) string {
	base := describeReason(reason)
	switch {
	case base != "" && detail != "":
		return base + " — " + detail
	case base != "":
		return base
	case detail != "":
		return detail
	default:
		return ""
	}
}

// describeReason returns the constant-text portion of a failure description.
// It deliberately uses simple, operator-friendly wording.
//
//nolint:gocyclo // a flat switch is the clearest representation of the BR-DDB-005 vocabulary
func describeReason(reason string) string {
	switch reason {
	// Bomb defence (FR-7).
	case "bomb-defence rule 1":
		return "rule 1: compressed archive size exceeds the configured cap"
	case "bomb-defence rule 2":
		return "rule 2: cumulative extracted size exceeded the configured cap during streaming"
	case "bomb-defence rule 3":
		return "rule 3: compression ratio exceeded the configured cap during streaming (likely zip bomb)"
	case "bomb-defence rule 4":
		return "rule 4: archive contains more entries than the configured cap"
	case "bomb-defence rule 5":
		return "rule 5: an entry's directory nesting depth exceeds the cap"
	case "bomb-defence rule 6":
		return "rule 6: entry is a symbolic link, which is rejected for safety"
	case "bomb-defence rule 7":
		return "rule 7: entry path is absolute (rejected — path traversal protection)"
	case "bomb-defence rule 8":
		return "rule 8: entry path contains parent-directory traversal (..)"
	case "bomb-defence rule 9":
		return "rule 9: a single entry's declared decompressed size exceeds the cap"
	case "bomb-defence rule 10":
		return "rule 10: extraction did not complete within the configured time limit"

	// Path validation (FR-6).
	case PathReasonTraversal:
		return "entry path contains parent-directory traversal (..)"
	case PathReasonAbsolute:
		return "entry path is absolute or has a drive-letter prefix"
	case PathReasonEmpty:
		return "entry name is empty after normalisation"
	case PathReasonInvalidName:
		return "entry filename contains invalid characters (control chars or oversize)"

	// Unsupported features (FR-3.6).
	case "unsupported: " + FeatureEncryptedZIP:
		return "ZIP is encrypted; this service does not extract encrypted archives"
	case "unsupported: " + FeatureMultiDisk:
		return "ZIP is a multi-disk archive; this service only supports single-volume archives"
	case "unsupported: " + FeatureDeflate64:
		return "ZIP uses Deflate64 compression, which is not supported"

	// Retry exhaustion (FR-12).
	case "retries exhausted: " + TransientClassThrottling:
		return "AWS rate-limited the request 3 times in a row; usually transient — check throttle source"
	case "retries exhausted: " + TransientClass5xx:
		return "AWS returned 5xx 3 times in a row; usually transient — check service health"
	case "retries exhausted: " + TransientClassTimeout:
		return "AWS timed out 3 times in a row; check network latency"
	case "retries exhausted: " + TransientClassNetwork:
		return "Network error to AWS 3 times in a row; check VPC egress / DNS"

	// Misc.
	case "drain canceled":
		return "pod received SIGTERM during this extraction; redelivery is idempotent"
	case "permanent: source-download-failed":
		return "source archive could not be downloaded (4xx from S3 — likely missing or access denied)"
	}

	// Recognise prefixes we did not enumerate exhaustively.
	switch {
	case strings.HasPrefix(reason, "corrupt-zip"):
		return "ZIP central directory could not be parsed — archive is malformed or truncated"
	case strings.HasPrefix(reason, "schema:"):
		return "SQS message did not match the expected claim-check schema (missing required field)"
	case strings.HasPrefix(reason, "permanent:"):
		return fmt.Sprintf("AWS returned a non-retryable error (%s)", strings.TrimPrefix(reason, "permanent: "))
	case strings.HasPrefix(reason, "archive aborted:"):
		return "entry failed because the archive was aborted by a prior violation"
	}
	return ""
}
