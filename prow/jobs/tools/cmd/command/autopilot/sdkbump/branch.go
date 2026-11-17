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
	"regexp"
	"strings"
)

// ErrRefNotAllowed: the workflow pushes to one ref per service, never main.
var ErrRefNotAllowed = errors.New("sdk-bump pushes only to refs/heads/autopilot/<service>/sdk-bump")

var (
	shaRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	headRE = regexp.MustCompile(`^autopilot/[a-z0-9][a-z0-9-]*/sdk-bump$`)
)

// Branch is the org branch the bump for service lives on.
func Branch(service string) string { return "autopilot/" + service + "/sdk-bump" }

// BranchRef is Branch as a full ref.
func BranchRef(service string) string { return "refs/heads/" + Branch(service) }

// checkPushRef is the guard between a computed ref and git push.
func checkPushRef(service, ref string) error {
	if len(service) > maxServiceLen || !serviceRE.MatchString(service) || ref != BranchRef(service) {
		return fmt.Errorf("%w: service %q, ref %q", ErrRefNotAllowed, service, ref)
	}
	return nil
}

// pushRefspec is the only function that builds a refspec for git push: one
// commit to the service's bump branch. No "+" (force is the lease, see
// gitOps.push), no wildcard, no second ref.
func pushRefspec(service, sha string) (string, error) {
	if !shaRE.MatchString(sha) {
		return "", fmt.Errorf("%w: %q is not a commit SHA", ErrRefNotAllowed, sha)
	}
	ref := BranchRef(service)
	if err := checkPushRef(service, ref); err != nil {
		return "", err
	}
	return sha + ":" + ref, nil
}

// canonicalRemote is the only remote --write may push to. The token comes
// from GIT_ASKPASS; the user name is GitHub's placeholder, not a secret.
func canonicalRemote(org, service string) string {
	return "https://x-access-token@github.com/" + org + "/" + service + "-controller.git"
}

// isLocalRemote reports a file:// remote, which tests and local verification
// push to.
func isLocalRemote(r string) bool { return strings.HasPrefix(r, "file://") }
