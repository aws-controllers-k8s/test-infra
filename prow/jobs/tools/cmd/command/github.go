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

package command

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v63/github"
)

const (
	baseBranch = "main"
)

var (
	defaultProwAutoGenLabel = "prow/auto-gen"
)

// getRef returns the commit branch reference object if it exists or creates it
// from the base branch before returning it.
func getGitRef(ctx context.Context, client *github.Client, sourceOwner, sourceRepo, commitBranch, baseBranch string) (ref *github.Reference, err error) {
	if ref, _, err = client.Git.GetRef(ctx, sourceOwner, sourceRepo, "refs/heads/"+commitBranch); err == nil {
		return ref, nil
	}

	// We consider that an error means the branch has not been found and needs to
	// be created.
	if commitBranch == baseBranch || baseBranch == "" {
		return nil, fmt.Errorf("the commit branch does not exist and base branch is equal to commit branch: %s", baseBranch)
	}

	var baseRef *github.Reference
	if baseRef, _, err = client.Git.GetRef(ctx, sourceOwner, sourceRepo, "refs/heads/"+baseBranch); err != nil {
		return nil, err
	}
	newRef := &github.Reference{Ref: github.String("refs/heads/" + commitBranch), Object: &github.GitObject{SHA: baseRef.Object.SHA}}
	ref, _, err = client.Git.CreateRef(ctx, sourceOwner, sourceRepo, newRef)
	return ref, err
}

// getTree generates the tree to commit based on the given files and the commit
// of the ref you got in getRef.
// sourceFiles format: "file1:PATH_TO_FILE1,file2:PATH_TO_FILE2..."
func getGitTree(ctx context.Context, client *github.Client, ref *github.Reference, sourceFiles, sourceOwner, sourceRepo string) (tree *github.Tree, err error) {
	// Create a tree with what to commit.
	entries := []*github.TreeEntry{}

	// Load each file into the tree.
	for _, fileArg := range strings.Split(sourceFiles, ",") {
		file, content, err := getFileContent(fileArg)
		if err != nil {
			return nil, err
		}

		entry := &github.TreeEntry{
			Path: github.String(file),
			Type: github.String("blob"),
			Mode: github.String("100644"),
		}

		// Binary files must be uploaded as blobs with base64 encoding
		// to prevent corruption during JSON serialization
		if isBinaryFile(file) {
			blob := &github.Blob{
				Content:  github.String(base64.StdEncoding.EncodeToString(content)),
				Encoding: github.String("base64"),
			}
			createdBlob, _, err := client.Git.CreateBlob(ctx, sourceOwner, sourceRepo, blob)
			if err != nil {
				return nil, fmt.Errorf("failed to create blob for %s: %v", file, err)
			}
			entry.SHA = createdBlob.SHA
		} else {
			// Text files can use inline content
			entry.Content = github.String(string(content))
		}

		entries = append(entries, entry)
	}

	tree, _, err = client.Git.CreateTree(ctx, sourceOwner, sourceRepo, *ref.Object.SHA, entries)
	return tree, err
}

// getFileContent loads the local content of a file and return the target name
// of the file in the target repository and its contents.
// fileArg format: "filename:PATH_TO_FILE"
func getFileContent(fileArg string) (targetName string, b []byte, err error) {
	var localFile string
	files := strings.Split(fileArg, ":")
	switch {
	case len(files) < 1:
		return "", nil, fmt.Errorf("files to commit not submitted")
	case len(files) == 1:
		localFile = files[0]
		targetName = files[0]
	default:
		localFile = files[0]
		targetName = files[1]
	}

	b, err = os.ReadFile(localFile)
	return targetName, b, err
}

// isBinaryFile returns true if the file should be treated as binary
// based on its extension to prevent JSON serialization corruption.
func isBinaryFile(filename string) bool {
	ext := filepath.Ext(filename)
	binaryExtensions := []string{".gz", ".zip", ".tar", ".tgz", ".bz2", ".xz", ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".bin"}
	for _, binExt := range binaryExtensions {
		if ext == binExt {
			return true
		}
	}
	return false
}

// pushCommit creates the commit in the given reference using the given tree.
func pushCommit(ctx context.Context, client *github.Client, ref *github.Reference, tree *github.Tree, sourceOwner, sourceRepo, commitMessage string) (err error) {
	// Get the parent commit to attach the commit to.
	parent, _, err := client.Repositories.GetCommit(ctx, sourceOwner, sourceRepo, *ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	// This is not always populated, but is needed.
	parent.Commit.SHA = parent.SHA

	// Create the commit using the tree.
	commit := &github.Commit{Message: github.String(commitMessage), Tree: tree, Parents: []*github.Commit{parent.Commit}}
	opts := github.CreateCommitOptions{}
	newCommit, _, err := client.Git.CreateCommit(ctx, sourceOwner, sourceRepo, commit, &opts)
	if err != nil {
		return err
	}

	// Attach the commit to the master branch.
	ref.Object.SHA = newCommit.SHA
	_, _, err = client.Git.UpdateRef(ctx, sourceOwner, sourceRepo, ref, false)
	return err
}

func createPR(ctx context.Context, client *github.Client, prSubject, commitBranch, prDescription, sourceOwner, sourceRepo, baseBranch string) error {
	if prSubject == "" {
		return fmt.Errorf("missing pr title flag; skipping PR creation")
	}

	newPR := &github.NewPullRequest{
		Title:               github.String(prSubject),
		Head:                github.String(commitBranch),
		Base:                github.String(baseBranch),
		Body:                github.String(prDescription),
		MaintainerCanModify: github.Bool(true),
	}

	createdPR, _, err := client.PullRequests.Create(ctx, sourceOwner, sourceRepo, newPR)
	if err != nil {
		return err
	}

	// Update the PR labels

	// Github API ¯\_(ツ)_/¯
	prWithLabels := &github.PullRequest{
		Labels: []*github.Label{
			&github.Label{
				Name: &defaultProwAutoGenLabel,
			},
		},
	}
	_, _, err = client.PullRequests.Edit(
		ctx,
		sourceOwner,
		sourceRepo,
		int(*createdPR.ID),
		prWithLabels,
	)
	if err != nil {
		return nil
	}

	return nil
}

func createGithubIssue(owner, repo, title, body string, labels []string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("enviroment variable GITHUB_TOKEN is not provided")
	}

	client := github.NewClient(nil).WithAuthToken(token)

	newIssueRequest := &github.IssueRequest{
		Title:  github.String(title),
		Body:   github.String(body),
		Labels: &labels,
	}

	_, _, err := client.Issues.Create(context.Background(), owner, repo, newIssueRequest)
	if err != nil {
		return err
	}

	return nil
}
