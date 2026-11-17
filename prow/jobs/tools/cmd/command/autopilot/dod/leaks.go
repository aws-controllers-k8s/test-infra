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

package dod

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

type leakRule struct {
	sig    string
	re     *regexp.Regexp
	secret bool // fails everywhere; otherwise an identifier (decision 14)
}

// leakRules are matched on every added line, commit message and the PR body.
// A rule's first non-empty submatch, else the whole match, is the value.
var leakRules = []leakRule{
	{"AWS_ACCESS_KEY_ID", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), true},
	{"AWS_SECRET_ACCESS_KEY", regexp.MustCompile(`(?i)aws.{0,20}secret.{0,20}?['"=:\s]([A-Za-z0-9/+]{40})(?:[^A-Za-z0-9/+]|$)`), true},
	{"AWS_SESSION_TOKEN", regexp.MustCompile(`\b(?:FwoGZXIvYXdzE|IQoJb3JpZ2luX2Vj)[A-Za-z0-9/+=]{40,}`), true},
	{"PRIVATE_KEY", regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY(?: BLOCK)?-----`), true},
	{"GITHUB_TOKEN", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b`), true},
	{"SLACK_TOKEN", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), true},
	{"AWS_ACCOUNT_ID", regexp.MustCompile(`arn:aws[a-zA-Z-]*:[a-z0-9-]*:[a-z0-9-]*:(\d{12}):|(?i:account[ _-]?id)["'\s:=]+(\d{12})\b`), false},
}

// exampleValues are AWS documentation examples, which the API models (and so
// generated docs) are full of.
var exampleValues = map[string]bool{
	// Split so secret scanners do not flag this file.
	"AKIA" + "IOSFODNN7EXAMPLE": true, "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYEXAMPLEKEY": true,
	"000000000000": true, "111111111111": true, "123456789012": true, "111122223333": true,
	"444455556666": true, "555555555555": true, "777788889999": true, "999999999999": true,
	"012345678901": true, "210987654321": true, "123456789013": true,
}

func leakValue(m []string) string {
	for _, s := range m[1:] {
		if s != "" {
			return s
		}
	}
	return m[0]
}

func redact(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "match sha256:" + hex.EncodeToString(sum[:])[:12]
}

// scanText adds one finding per rule match. generated relaxes identifiers to
// inventory.
func scanText(findings []Finding, file string, line int, text string, generated bool) []Finding {
	for _, r := range leakRules {
		for _, m := range r.re.FindAllStringSubmatch(text, -1) {
			v := leakValue(m)
			if exampleValues[v] {
				continue
			}
			findings = append(findings, Finding{
				Signature: r.sig, File: file, Line: line, Detail: redact(v), Fails: r.secret || !generated,
			})
		}
	}
	return findings
}

func scanBlock(findings []Finding, file, text string) []Finding {
	for i, l := range strings.Split(text, "\n") {
		findings = scanText(findings, file, i+1, l, false)
	}
	return findings
}

// checkLeaks is the leak inventory (owner scope; design §9 "account IDs or
// ARNs in PR text").
func checkLeaks(files []changedFile, commits []commit, prBody []byte) Check {
	var findings []Finding
	for _, f := range files {
		if f.Binary {
			continue
		}
		for _, l := range f.Added {
			findings = scanText(findings, f.Path, l.N, l.Text, f.Generated)
		}
	}
	for _, c := range commits {
		short := c.SHA
		if len(short) > 12 {
			short = short[:12]
		}
		findings = scanBlock(findings, "commit:"+short, c.Message)
	}
	if prBody != nil {
		findings = scanBlock(findings, "pr-body", string(prBody))
	}
	return newCheck(CheckLeaks, findings)
}
