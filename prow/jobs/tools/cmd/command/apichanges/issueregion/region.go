// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package issueregion locates the generated region of a detect-api-changes
// issue body: the bytes between the begin and end markers that the detector
// owns and rewrites. Text outside it belongs to maintainers. The detector and
// the ACK Autopilot issue finder both use this package, so the markers they
// look for cannot drift apart.
package issueregion

import "strings"

// The markers that bound the generated region, each on a line of its own.
const (
	Begin = "<!-- ack-api-change-begin -->"
	End   = "<!-- ack-api-change-end -->"
)

// Shape is what Find found.
type Shape int

const (
	// Absent: neither marker appears in the body.
	Absent Shape = iota
	// Unique: exactly one begin and one end marker, each on its own line,
	// begin first.
	Unique
	// Malformed: any other arrangement.
	Malformed
)

// Find returns the half-open byte range from the begin marker through the end
// marker. Anything but Unique is reported rather than guessed at: refusing to
// read or refresh is safer than misattributing a maintainer's text. Raw marker
// occurrences are counted, so a marker in prose also makes the body malformed.
// Marker lines may have trailing spaces, tabs or a \r (a body edited in the
// GitHub web UI comes back CRLF-terminated).
func Find(body string) (start, end int, shape Shape) {
	begins := strings.Count(body, Begin)
	ends := strings.Count(body, End)
	if begins == 0 && ends == 0 {
		return 0, 0, Absent
	}
	if begins != 1 || ends != 1 {
		return 0, 0, Malformed
	}
	start = strings.Index(body, Begin)
	endAt := strings.Index(body, End)
	if endAt < start ||
		!isCompleteLine(body, start, len(Begin)) ||
		!isCompleteLine(body, endAt, len(End)) {
		return 0, 0, Malformed
	}
	return start, endAt + len(End), Unique
}

// Region returns the generated region, markers included, when the body has
// exactly one well-formed region.
func Region(body string) (string, bool) {
	start, end, shape := Find(body)
	if shape != Unique {
		return "", false
	}
	return body[start:end], true
}

// isCompleteLine reports whether body[at:at+n] is the whole of its line, give or
// take trailing spaces, tabs and a \r.
func isCompleteLine(body string, at, n int) bool {
	if at > 0 && body[at-1] != '\n' {
		return false
	}
	rest := body[at+n:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.Trim(rest, " \t\r") == ""
}
