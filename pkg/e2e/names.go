package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// RandomName generates a unique resource name with the given prefix and a random
// suffix, truncated to maxLen. The format is "{prefix}-{randomHex}".
func RandomName(prefix string, maxLen int) string {
	suffix := randomHex(8)
	name := fmt.Sprintf("%s-%s", prefix, suffix)
	if len(name) > maxLen {
		name = name[:maxLen]
	}
	return strings.TrimRight(name, "-")
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
