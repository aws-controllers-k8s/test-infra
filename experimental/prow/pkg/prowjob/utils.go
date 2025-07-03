package prowjob

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// generateJobID generates a unique job identifier
func generateJobID() string {
	timestamp := time.Now().Unix()
	randomBytes := make([]byte, 4)
	rand.Read(randomBytes)
	randomHex := hex.EncodeToString(randomBytes)
	return fmt.Sprintf("%d-%s", timestamp, randomHex)
}

// mapToString converts a map to a string representation
func mapToString(m map[string]string) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// parseTimeout parses a timeout string to a duration
func parseTimeout(timeout string, defaultTimeout time.Duration) (time.Duration, error) {
	if timeout == "" {
		return defaultTimeout, nil
	}
	return time.ParseDuration(timeout)
}

// parseResourceQuantity safely parses resource quantity string with reasonable fallbacks
func parseResourceQuantity(quantity string) resource.Quantity {
	if quantity == "" {
		return resource.MustParse("0")
	}
	
	q, err := resource.ParseQuantity(quantity)
	if err != nil {
		log.Printf("Warning: Failed to parse resource quantity '%s': %v. Using fallback value.", quantity, err)
		
		lowerQuantity := strings.ToLower(quantity)
		if strings.Contains(lowerQuantity, "cpu") || strings.Contains(lowerQuantity, "core") || 
		   strings.Contains(lowerQuantity, "m") && !strings.Contains(lowerQuantity, "mi") && 
		   !strings.Contains(lowerQuantity, "gi") && !strings.Contains(lowerQuantity, "ti") {
			return resource.MustParse("100m")
		} else if strings.Contains(lowerQuantity, "memory") || strings.Contains(lowerQuantity, "mi") || 
				  strings.Contains(lowerQuantity, "gi") || strings.Contains(lowerQuantity, "ti") ||
				  strings.Contains(lowerQuantity, "byte") {
			return resource.MustParse("128Mi")
		}
		return resource.MustParse("100m")
	}
	return q
}