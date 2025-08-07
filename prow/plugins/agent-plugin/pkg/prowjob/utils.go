// Copyright 2020 Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// String returns a pointer to the string value provided
func String(v string) *string {
	return &v
}

// Bool return a point to the boolean value provided
func Bool(v bool) *bool {
	return &v
}

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
