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

package dispatcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/ledger"
)

// runtimeReleaseBranch prefixes the head branch of ack-bot's runtime and
// code-generator release PRs (cd/auto-generate/auto-generate-controllers.sh
// DEFAULT_PR_SOURCE_BRANCH).
const runtimeReleaseBranch = "ack-bot/rt-"

// runtimeReleaseRepos returns the repos of QUEUED items in which an ack-bot
// runtime-release PR opened in the last RuntimeReleaseBackoff is open: a bump
// there would conflict with it (design 5.1). A repo whose PRs cannot be read
// is held too (fail closed), with one problem line.
func runtimeReleaseRepos(ctx context.Context, gh PRClient, items map[string]*ledger.WorkItem, now time.Time) (map[string]bool, []string) {
	repos := map[string]bool{}
	for _, w := range items {
		if _, ok := lanes[w.ChangeClass]; ok && w.State == ledger.Queued {
			repos[w.Repo] = true
		}
	}
	names := make([]string, 0, len(repos))
	for r := range repos {
		names = append(names, r)
	}
	sort.Strings(names)
	held := map[string]bool{}
	var problems []string
	for _, repo := range names {
		prs, err := gh.OpenPRs(ctx, repo)
		if err != nil {
			held[repo] = true
			problems = append(problems, fmt.Sprintf("open PRs of %s: %v; held as %s", repo, err, CapRuntimeRelease))
			continue
		}
		for _, p := range prs {
			created := mustParse(p.CreatedAt, time.Time{})
			if p.Author == "ack-bot" && strings.HasPrefix(p.HeadRef, runtimeReleaseBranch) && now.Sub(created) < RuntimeReleaseBackoff {
				held[repo] = true
				break
			}
		}
	}
	return held, problems
}
