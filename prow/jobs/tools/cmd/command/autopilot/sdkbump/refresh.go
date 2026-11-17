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
	"context"
	"errors"
)

// refresh re-evaluates the merge block of the open bump PR without
// regenerating (decision 6). Plan 7 runs it when a dependent changes state.
func refresh(ctx context.Context, o Options) Report {
	g := gitOps{dir: o.ControllerDir, run: o.Run}
	prs, err := o.Forge.PRsForBranch(ctx, o.repo(), Branch(o.Service))
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "GITHUB_READ_FAILED", "")
	}
	open, halt := pickPR(prs, o)
	if halt != nil {
		return *halt
	}
	if open == nil {
		return stop(OutcomeNoChange, "NO_OPEN_PR", "")
	}
	head, exists, err := g.remoteBranch(ctx, o.remote(), o.Service)
	if err != nil {
		o.logf("%v", err)
		return stop(OutcomeFailed, "FETCH_FAILED", open.URL)
	}
	if !exists || (open.HeadSHA != "" && head != open.HeadSHA) {
		return stop(OutcomeFailed, "BRANCH_MOVED", open.URL)
	}
	if _, err := refreshGate(ctx, o, g, *open, head); err != nil {
		o.logf("%v", err)
		if errors.Is(err, errMarkersMissing) {
			return stop(OutcomeNeedsHuman, "BODY_MARKERS_MISSING", open.URL)
		}
		return stop(OutcomeFailed, "PR_UPDATE_FAILED", open.URL)
	}
	return stop(OutcomePROpened, "REFRESHED", open.URL)
}
