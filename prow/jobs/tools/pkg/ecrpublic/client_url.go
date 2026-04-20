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

package ecrpublic

import "fmt"

const (
	// ecrPublicDNS is the DNS name for the ECR Public registry.
	ecrPublicDNS = "public.ecr.aws"
)

// builGetTokenURL returns the URL to get a token from the ECR Public
// registry.
func builGetTokenURL() string {
	return fmt.Sprintf("https://%s/token/", ecrPublicDNS)
}

// buildGetManifestsURL returns the URL to get the manifest for a
// specific version of a repository in the ECR Public registry.
func buildGetManifestsURL(repository, version string) string {
	return fmt.Sprintf("https://%s/%s/manifests/%s", ecrPublicDNS, repository, version)
}

// buildListTagsURL returns the URL to get the tags for a repository
// in the ECR Public registry.
func buildListTagsURL(repository string) string {
	return fmt.Sprintf("https://%s/%s/tags/list", ecrPublicDNS, repository)
}

// buildGetBlobURL returns the URL to get the blob for a specific
// digest in the ECR Public registry.
func buildGetBlobURL(repository, digest string) string {
	return fmt.Sprintf("https://%s/%s/blobs/%s", ecrPublicDNS, repository, digest)
}

func buildURL(repository string) string {
	return fmt.Sprintf("https://%s/%s", ecrPublicDNS, repository)
}
