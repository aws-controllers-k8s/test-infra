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

package prowlaunch

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ModeBump and ModeRefresh are sdk-bump's modes (Plan 6 sdkbump.ModeBump,
// sdkbump.ModeRefresh; a test keeps them equal).
const (
	ModeBump    = "bump"
	ModeRefresh = "refresh"
)

// validators lists every arg the Dispatcher may pass and what its value must
// look like. Args reach the pod as container args or environment variables,
// never through a shell, and are still kept to these shapes. An arg a
// workflow accepts but that has no validator here (add-field's model,
// aws-sdk-version) can never be passed by the Dispatcher.
var validators = map[string]*regexp.Regexp{
	"service":       regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`),
	"resource":      regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,127}$`),
	"field":         regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z][A-Za-z0-9]*)*$`),
	"work-item":     regexp.MustCompile(`^sha256:[0-9a-f]{16}$`),
	"change-class":  regexp.MustCompile(`^(SDK-bump|config-only)$`),
	"sdk-to":        regexp.MustCompile(`^service/[a-z0-9]{1,63}/v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`),
	"detector-work": regexp.MustCompile(`^(create-only|not-generated|server-default)$`),
	"mode":          regexp.MustCompile(`^(` + ModeBump + `|` + ModeRefresh + `)$`),
	// The dod job's (Plan 9): from the GitHub API, never from PR text.
	"pr":        regexp.MustCompile(`^[1-9][0-9]{0,9}$`),
	"head-sha":  regexp.MustCompile(`^[0-9a-f]{40}$`),
	"base-sha":  regexp.MustCompile(`^[0-9a-f]{40}$`),
	"pr-author": regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?(\[bot\])?$`),
}

// workflowValidators override validators for one workflow's args. Triage
// (Plan 8) passes the item's detector class and any detector work name,
// and its finding class; add-field and sdk-bump keep the shapes above.
// dispatcher.triageArgs holds items to the same shapes.
var workflowValidators = map[string]map[string]*regexp.Regexp{
	"add-resource": {"change-class": regexp.MustCompile(`^new-resource$`)},
	"dod":          {"change-class": regexp.MustCompile(`^(SDK-bump|config-only|new-resource)$`)},
	"triage": {
		"change-class":  regexp.MustCompile(`^(config-only|hand-written-code)$`),
		"detector-work": regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`),
		"finding-class": regexp.MustCompile(`^(spec-field|status-field|lifecycle-field)$`),
	},
}

// maxArgLen bounds every arg value.
const maxArgLen = 256

// classesOf is the change classes each workflow is started for. A triage
// run (Plan 8) carries the item's detector class.
var classesOf = map[string][]string{
	"add-field": {"config-only"},
	"sdk-bump":  {"SDK-bump"},
	"triage":    {"config-only", "hand-written-code"},
	// Plan 9.
	"add-resource": {"new-resource"},
	"dod":          {"SDK-bump", "config-only", "new-resource"},
}

func quoteOr(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, " or ")
}

// checkArgs checks args against the workflow's required_args and
// optional_args (as agent-plugin's ValidateRequiredArgs and ValidateArgs do
// for /agent) and against validators. workItem is the key hash the launch id
// names; the work-item arg must equal it.
func checkArgs(name string, w Workflow, args map[string]string, workItem string) error {
	for _, a := range w.RequiredArgs {
		if _, ok := args[a]; !ok {
			return fmt.Errorf("missing required arg %q", a)
		}
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(w.RequiredArgs, k) && !slices.Contains(w.OptionalArgs, k) {
			return fmt.Errorf("arg %q is not in the workflow's required_args or optional_args", k)
		}
		re, ok := workflowValidators[name][k]
		if !ok {
			re, ok = validators[k]
		}
		if !ok {
			return fmt.Errorf("arg %q is not one the Dispatcher may pass", k)
		}
		if len(args[k]) > maxArgLen || !re.MatchString(args[k]) {
			return fmt.Errorf("arg %q has a value of the wrong shape (len=%d)", k, len(args[k]))
		}
	}
	if args["work-item"] != workItem {
		return fmt.Errorf("arg work-item must be the launch id's key hash %s", workItem)
	}
	if !slices.Contains(classesOf[name], args["change-class"]) {
		return fmt.Errorf("arg change-class must be %s for %s", quoteOr(classesOf[name]), name)
	}
	return nil
}
