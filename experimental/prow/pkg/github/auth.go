package github

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/google/go-github/v57/github"
	"github.com/jferrl/go-githubauth"
	"golang.org/x/oauth2"
)

// NewAppAuthClient initializes a GitHub client with GitHub App authentication
func NewAppAuthClient(appPath string) (Client, error) {
	// Read the GitHub App ID - we expect it to be directly in /etc/github/app-id
	appIDBytes, err := os.ReadFile(appPath + "/app-id")
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App ID: %w", err)
	}
	appID := strings.TrimSpace(string(appIDBytes))
	
	appIDInt, err := strconv.ParseInt(appID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub App ID: %w", err)
	}
	
	privateKeyBytes, err := os.ReadFile(appPath + "/private-key")
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub App private key: %w", err)
	}
	
	// Create a GitHub App token source
	appTokenSource, err := githubauth.NewApplicationTokenSource(appIDInt, privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub App token source: %w", err)
	}
	
	ctx, cancel := ContextWithDefaultTimeout()
	defer cancel()
	
	httpClient := oauth2.NewClient(ctx, appTokenSource)
	appClient := github.NewClient(httpClient)
	
	installations, _, err := appClient.Apps.ListInstallations(ctx, &github.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list GitHub App installations: %w", err)
	}
	
	if len(installations) == 0 {
		return nil, fmt.Errorf("no GitHub App installations found")
	}
	
	installID := installations[0].GetID()
	log.Printf("Using GitHub App installation ID: %d for %s", installID, installations[0].GetAccount().GetLogin())
	installationTokenSource := githubauth.NewInstallationTokenSource(installID, appTokenSource)
	ctxInstall, cancelInstall := ContextWithDefaultTimeout()
	defer cancelInstall()
	
	installClient := github.NewClient(oauth2.NewClient(ctxInstall, installationTokenSource))
	
	_, _, err = installClient.Repositories.Get(ctxInstall, "ack-prow-staging", "community")
	if err != nil {
		return nil, fmt.Errorf("GitHub App installation validation failed: %w", err)
	}
	
	log.Printf("Successfully initialized GitHub App client for installation ID: %d", installID)
	return NewClient(installClient), nil
}