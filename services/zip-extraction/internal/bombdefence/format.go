package bombdefence

import "fmt"

func formatExceeded(label string, actual, limit int64) string {
	return fmt.Sprintf("%s %d exceeds cap %d", label, actual, limit)
}

func formatExceededInt(label string, actual, limit int) string {
	return fmt.Sprintf("%s %d exceeds cap %d", label, actual, limit)
}

func formatRatioExceeded(actual, limit float64) string {
	return fmt.Sprintf("compression ratio %.2fx exceeds cap %.2fx", actual, limit)
}
