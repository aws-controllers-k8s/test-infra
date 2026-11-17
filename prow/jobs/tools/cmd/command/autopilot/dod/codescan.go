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
	"encoding/json"
	"go/parser"
	"go/scanner"
	"go/token"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// riskyImports are design §9 control 4's imports.
var riskyImports = map[string]bool{"net": true, "net/http": true, "os/exec": true, "unsafe": true, "syscall": true}

var (
	urlLiteral  = regexp.MustCompile(`(?i)\b(?:https?|ftp|wss?)://`)
	ipLiteral   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	imdsAddress = regexp.MustCompile(`169\.254\.169\.254|fd00:ec2::254`)
	base64Blob  = regexp.MustCompile(`[A-Za-z0-9+/]{120,}={0,2}`)
)

// strictModuleClasses fail on a new module outside the SDK (design §6).
var strictModuleClasses = map[string]bool{ClassMechanical: true, ClassDocsOnly: true, ClassSDKBump: true}

func sdkModule(path string) bool {
	return path == "github.com/aws/aws-sdk-go-v2" || strings.HasPrefix(path, "github.com/aws/aws-sdk-go-v2/") ||
		path == "github.com/aws/smithy-go"
}

func goImports(src []byte) (map[string]bool, error) {
	out := map[string]bool{}
	if src == nil {
		return out, nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil {
			out[p] = true
		}
	}
	return out, nil
}

// goStringLiterals returns a Go file's string literals that look like a URL or
// an IP address. Comments are not scanned.
func goStringLiterals(src []byte) map[string]string {
	out := map[string]string{}
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return out
		}
		if tok != token.STRING {
			continue
		}
		switch {
		case urlLiteral.MatchString(lit):
			out[lit] = "NEW_URL_LITERAL"
		case ipLiteral.MatchString(lit):
			out[lit] = "NEW_IP_LITERAL"
		}
	}
}

func scanGo(f changedFile) []Finding {
	var out []Finding
	head, err := goImports(f.Head)
	if err != nil {
		return []Finding{{Signature: "GO_PARSE_FAILED", File: f.Path, Fails: true}}
	}
	base, _ := goImports(f.Base)
	for _, imp := range sortedKeys(head) {
		if riskyImports[imp] && !base[imp] {
			out = append(out, Finding{Signature: "NEW_RISKY_IMPORT", File: f.Path, Detail: "import " + imp, Fails: true})
		}
	}
	if f.Generated {
		return out
	}
	before := goStringLiterals(f.Base)
	for lit, sig := range goStringLiterals(f.Head) {
		if _, ok := before[lit]; !ok {
			out = append(out, Finding{Signature: sig, File: f.Path, Detail: "literal " + lit, Fails: true})
		}
	}
	return out
}

func goSumHashes(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 3 {
			out[parts[0]+" "+parts[1]] = parts[2]
		}
	}
	return out
}

func scanGoSum(f changedFile) []Finding {
	var out []Finding
	base, head := goSumHashes(f.Base), goSumHashes(f.Head)
	for _, k := range sortedKeys(head) {
		if old, ok := base[k]; ok && old != head[k] {
			out = append(out, Finding{Signature: "GOSUM_HASH_CHANGED", File: f.Path, Detail: k, Fails: true})
		}
	}
	return out
}

func scanGoMod(class string, f changedFile) (out []Finding, sensitive bool) {
	parse := func(b []byte) *modfile.File {
		if b == nil {
			return &modfile.File{}
		}
		// Parse, not ParseLax: ParseLax drops replace directives.
		m, err := modfile.Parse("go.mod", b, nil)
		if err != nil {
			return nil
		}
		return m
	}
	base, head := parse(f.Base), parse(f.Head)
	if base == nil || head == nil {
		return []Finding{{Signature: "GOMOD_UNPARSEABLE", File: f.Path, Fails: true}}, false
	}
	had := map[string]bool{}
	for _, r := range base.Require {
		had[r.Mod.Path] = true
	}
	for _, r := range head.Require {
		if !had[r.Mod.Path] && !sdkModule(r.Mod.Path) {
			out = append(out, Finding{Signature: "NEW_GO_MODULE", File: f.Path, Detail: r.Mod.Path + " " + r.Mod.Version, Fails: strictModuleClasses[class]})
		}
	}
	replaced := map[string]bool{}
	for _, r := range base.Replace {
		replaced[r.Old.Path+" => "+r.New.Path] = true
	}
	for _, r := range head.Replace {
		if k := r.Old.Path + " => " + r.New.Path; !replaced[k] {
			out = append(out, Finding{Signature: "GOMOD_REPLACE_ADDED", File: f.Path, Detail: k, Fails: true})
			sensitive = true
		}
	}
	return out, sensitive
}

// stringOrList decodes an IAM Action/Resource value.
func stringOrList(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

// wildcardGrants returns the Allow (action, resource) pairs where either side
// is "*", and whether any statement uses NotAction or NotResource.
func wildcardGrants(doc []byte) (map[string]bool, bool, error) {
	out := map[string]bool{}
	if doc == nil {
		return out, false, nil
	}
	var p struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal(doc, &p); err != nil {
		return nil, false, err
	}
	type stmt struct {
		Effect                                   string
		Action, NotAction, Resource, NotResource json.RawMessage
	}
	var stmts []stmt
	if err := json.Unmarshal(p.Statement, &stmts); err != nil {
		var one stmt
		if err := json.Unmarshal(p.Statement, &one); err != nil {
			return nil, false, err
		}
		stmts = []stmt{one}
	}
	negated := false
	for _, s := range stmts {
		negated = negated || len(s.NotAction) > 0 || len(s.NotResource) > 0
		if s.Effect != "Allow" {
			continue
		}
		for _, a := range stringOrList(s.Action) {
			for _, r := range stringOrList(s.Resource) {
				if a == "*" || r == "*" {
					out[a+" on "+r] = true
				}
			}
		}
	}
	return out, negated, nil
}

func scanIAMPolicy(f changedFile) []Finding {
	base, baseNeg, err := wildcardGrants(f.Base)
	if err != nil {
		base = map[string]bool{}
	}
	head, headNeg, err := wildcardGrants(f.Head)
	if err != nil {
		return []Finding{{Signature: "IAM_POLICY_UNPARSEABLE", File: f.Path, Fails: true}}
	}
	var out []Finding
	for _, g := range sortedKeys(head) {
		if !base[g] {
			out = append(out, Finding{Signature: "IAM_WIDENED", File: f.Path, Detail: g, Fails: true})
		}
	}
	if headNeg && !baseNeg {
		out = append(out, Finding{Signature: "IAM_NOT_ACTION", File: f.Path, Fails: true})
	}
	return out
}

// checkCodeScan is design §9 control 4, offline and deterministic.
func checkCodeScan(class string, files []changedFile) Check {
	var findings []Finding
	sensitive := false
	for _, f := range files {
		if f.Status == 'D' {
			continue
		}
		if f.Binary {
			// Nothing below (nor leak-inventory) can read it: fail closed.
			findings = append(findings, Finding{Signature: "BINARY_FILE", File: f.Path, Fails: true})
			continue
		}
		switch {
		case strings.HasSuffix(f.Path, ".go"):
			findings = append(findings, scanGo(f)...)
		case f.Path == "go.sum":
			findings = append(findings, scanGoSum(f)...)
		case f.Path == "go.mod":
			fs, s := scanGoMod(class, f)
			findings, sensitive = append(findings, fs...), sensitive || s
		case f.Path == "config/iam/recommended-inline-policy":
			findings = append(findings, scanIAMPolicy(f)...)
		}
		for _, l := range f.Added {
			if imdsAddress.MatchString(l.Text) {
				findings = append(findings, Finding{Signature: "IMDS_ADDRESS", File: f.Path, Line: l.N, Fails: true})
			}
			if f.Path != "go.sum" && base64Blob.MatchString(l.Text) {
				findings = append(findings, Finding{Signature: "BASE64_BLOB", File: f.Path, Line: l.N, Fails: true})
			}
			if f.Path == "config/iam/recommended-policy-arn" && strings.Contains(l.Text, "AdministratorAccess") {
				findings = append(findings, Finding{Signature: "IAM_ADMIN_POLICY", File: f.Path, Line: l.N, Fails: true})
			}
		}
	}
	c := newCheck(CheckCodeScan, findings)
	if sensitive {
		c.Labels = []string{LabelSensitivePath}
	}
	return c
}
