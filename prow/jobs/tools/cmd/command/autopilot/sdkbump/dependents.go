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

package sdkbump

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/issuefinder"
	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// ServerDefaultWork is the detector work Plan 4 gives a new Spec field that a
// read the controller calls returns.
const ServerDefaultWork = "server-default"

// Dependent is one server-default field that arrives with the bump.
type Dependent struct {
	KeyHash, Resource, FieldPath string
	State                        ledger.State
	Resolved                     bool
	How                          string // why it is resolved; "" when it is not
}

// Dependents are the bump's server-default dependents. Known is false when
// the ledger could not be read or does not hold the bump item: the merge is
// then blocked (fail closed).
type Dependents struct {
	Known bool
	Items []Dependent
}

// Unresolved counts the dependents that still need a decision.
func (d Dependents) Unresolved() int {
	n := 0
	for _, x := range d.Items {
		if !x.Resolved {
			n++
		}
	}
	return n
}

// Blocked reports whether the bump must not merge yet.
func (d Dependents) Blocked() bool { return !d.Known || d.Unresolved() > 0 }

// FindDependents lists the items that depend on bumpKey with work
// server-default, resolved when the ledger closed them or when generatorYAML
// (at the PR head) sets late_initialize or compare.is_ignored for the field.
func FindDependents(items map[string]*ledger.WorkItem, bumpKey string, generatorYAML []byte) Dependents {
	decided := fieldDecisions(generatorYAML)
	out := []Dependent{}
	for _, w := range items {
		if w.DetectorWork != ServerDefaultWork || !slices.Contains(w.DependsOn, bumpKey) {
			continue
		}
		d := Dependent{KeyHash: w.KeyHash, Resource: w.Resource, FieldPath: w.FieldPath, State: w.State}
		switch w.State {
		case ledger.WontFix:
			d.How = "closed as won't fix in the ledger"
		case ledger.Done:
			d.How = "done in the ledger"
		case ledger.Superseded:
			d.How = "superseded in the ledger"
		default:
			d.How = decided[decisionKey(w.Resource, w.FieldPath)]
		}
		d.Resolved = d.How != ""
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return slices.Compare([]string{a.Resource, a.FieldPath, a.KeyHash}, []string{b.Resource, b.FieldPath, b.KeyHash}) < 0
	})
	return Dependents{Known: true, Items: out}
}

func decisionKey(kind, field string) string {
	return strings.ToLower(kind) + "|" + strings.ToLower(field)
}

// fieldDecisions maps resource|field (lowercased) to the generator.yaml
// setting that decides it. A file that does not parse decides nothing.
func fieldDecisions(gen []byte) map[string]string {
	var cfg struct {
		Resources map[string]struct {
			Fields map[string]struct {
				LateInitialize *yaml.Node `yaml:"late_initialize"`
				Compare        *struct {
					IsIgnored bool `yaml:"is_ignored"`
				} `yaml:"compare"`
			} `yaml:"fields"`
		} `yaml:"resources"`
	}
	d := map[string]string{}
	if err := yaml.Unmarshal(gen, &cfg); err != nil {
		return d
	}
	for kind, r := range cfg.Resources {
		for path, f := range r.Fields {
			switch {
			case f.LateInitialize != nil:
				d[decisionKey(kind, path)] = "generator.yaml sets late_initialize"
			case f.Compare != nil && f.Compare.IsIgnored:
				d[decisionKey(kind, path)] = "generator.yaml sets compare.is_ignored"
			}
		}
	}
	return d
}

// ErrNoBumpItem: the ledger holds no open SDK-bump item for the service.
var ErrNoBumpItem = errors.New("no open SDK-bump item for the service")

// FromLedger returns the target and key hash of the service's one open
// SDK-bump item (not terminal, not escalated), for the manual job.
func FromLedger(items map[string]*ledger.WorkItem, service string) (sdkTo, workItem string, err error) {
	prefix := issuefinder.BumpKey(service, "")
	var found []*ledger.WorkItem
	for k, w := range items {
		if strings.HasPrefix(k, prefix) && w.ChangeClass == dod.ClassSDKBump && !w.State.Terminal() && w.State != ledger.Escalated {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 0:
		return "", "", fmt.Errorf("%w %s", ErrNoBumpItem, service)
	case 1:
		return strings.TrimPrefix(found[0].Key, prefix), found[0].KeyHash, nil
	}
	return "", "", fmt.Errorf("%d open SDK-bump items for %s", len(found), service)
}
