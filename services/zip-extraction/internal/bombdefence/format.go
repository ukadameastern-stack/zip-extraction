package bombdefence

import "fmt"

func formatExceeded(label string, actual, cap int64) string {
	return fmt.Sprintf("%s %d exceeds cap %d", label, actual, cap)
}

func formatExceededInt(label string, actual, cap int) string {
	return fmt.Sprintf("%s %d exceeds cap %d", label, actual, cap)
}

func formatRatioExceeded(actual, cap float64) string {
	return fmt.Sprintf("compression ratio %.2fx exceeds cap %.2fx", actual, cap)
}
