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

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultHTTPClientTimeout = 120 * time.Second
)

// Client is the interface that defines the methods to interact
// with the ECR Public registry.
//
// This interface is used to mock the ECR Public registry client
// in tests.
type ClientII interface {
	ListRepositoryTags(repository string) ([]string, error)
	GetRepositoryManifest(repository, version string) (*manifests, error)
	DownloadRepositoryBlob(service string, digest string) ([]byte, error)
}

// Client is a client for the ECR Public registry.
//
// It is used to interact with the ECR Public registry to list
// available versions of a given repository, get the manifest
// for a specific version.
//
// This client is thread-safe.
type Client struct {
	mu                  sync.RWMutex
	token               string
	tokenExpirationTime time.Time

	// httpClient is already thread-safe, so we don't need to
	// protect it with a mutex.
	httpClient http.Client
	userAgent  string

	// Cache the number of hits and misses to the ECR Public
	// registry. This is useful for debugging and monitoring.
	// This is not implemented yet.
	Hits   uint64
	Misses uint64
}

// New returns a new ECR Public registry client.
func New() *Client {
	return &Client{
		userAgent: "ack-operator/v0.0.0",
		httpClient: http.Client{
			Timeout: defaultHTTPClientTimeout,
		},
	}
}

// getTokenResponse is a helper struct to unmarshal the response
// from the ECR Public registry token endpoint.
type getTokenResponse struct {
	Token string `json:"token"`
}

// getToken returns a token that can be used to authenticate
// requests to the ECR Public registry. The token is cached
// and will be reused until it is close to expiration.
func (c *Client) getToken() (string, error) {
	// If we have a token that is not close to expiration, return it
	token, expiringSoon := c.getTokenFromCache()
	if token != "" && !expiringSoon {
		return token, nil
	}

	req, err := http.NewRequest("GET", builGetTokenURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.userAgent)

	// Request a new token
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// unmashal the response into a getTokenResponse struct
	var response getTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", err
	}
	tokenExpirationTime, err := getTokenExpirationTime(response.Token)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = response.Token
	c.tokenExpirationTime = tokenExpirationTime

	return c.token, nil
}

// getTokenFromCache returns the cached token and a boolean indicating
// whether the token is expiring soon.
func (c *Client) getTokenFromCache() (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token, time.Until(c.tokenExpirationTime) < 5*time.Minute
}

// listTagsResponse is a helper struct to unmarshal the response
// from the ECR Public registry tags endpoint.
type listTagsResponse struct {
	Tags      []string `json:"tags"`
	NextToken string   `json:"nextToken,omitempty"`
}

// ListRepositoryTags returns a list of tags for a given public repository.
func (c *Client) ListRepositoryTags(repository string) ([]string, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, err
	}

	ans := make([]string, 0, 3000)

	link := ""
	var url string
	var repo listTagsResponse
	for {

		if link != "" {
			url = buildURL(link)
		} else {
			url = buildListTagsURL(repository)
		}

		// set Authorization header
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("User-Agent", c.userAgent)

		// Execute the request
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		// unmashal the response into a ECRPublicRepository struct
		if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
			return nil, err
		}
		ans = append(ans, repo.Tags...)

		if temp, ok := resp.Header["Link"]; ok {
			link = temp[0]
			end := strings.Index(link, ">")

			link = link[1:end]
		} else {
			break
		}
	}

	return ans, nil
}

// manifests is a helper struct to unmarshal the response
// from the ECR Public registry manifests endpoint.
type manifests struct {
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int    `json:"size"`
	} `json:"layers"`
}

// GetRepositoryManifest returns the manifest for a specific version
// of a repository in the ECR Public registry.
func (c *Client) GetRepositoryManifest(repository, version string) (*manifests, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, err
	}

	url := buildGetManifestsURL(repository, version)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var manifests manifests
	if err := json.NewDecoder(resp.Body).Decode(&manifests); err != nil {
		return nil, err
	}

	return &manifests, nil
}

// DownloadRepositoryBlob downloads the blob for a specific digest
// from the ECR Public registry.
func (c *Client) DownloadRepositoryBlob(service string, digest string) ([]byte, error) {
	token, err := c.getToken()
	if err != nil {
		return nil, err
	}

	url := buildGetBlobURL(service, digest)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	req.Header.Set("User-Agent", c.userAgent)

	// Download the tarball
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

/*
	// Extract the tarball
	content, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	defer content.Close()

	// Create a new tar reader
	tarReader := tar.NewReader(content)

	crds := map[string]*extv1.CustomResourceDefinition{}

	// Iterate through the files in the tarball
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return nil, err
		}

		// Check if the file is in the ./crd/ directory
		if strings.HasPrefix(header.Name, service+"-chart/crds/") {
			if strings.Contains(header.Name, "adopted") || strings.Contains(header.Name, "export") {
				continue
			}
			var buffer bytes.Buffer
			// Copy the file content to the buffer
			if _, err := io.Copy(&buffer, tarReader); err != nil {
				return nil, err
			}

			// unmarshal the CRD
			var crd extv1.CustomResourceDefinition
			if err := yaml.Unmarshal(buffer.Bytes(), &crd); err != nil {
				return nil, err
			}

			fileNameTrimmed := strings.TrimPrefix(header.Name, service+"-chart/crds/")
			crds[fileNameTrimmed] = &crd
		}
	}

	// Return the buffer containing the extracted files
	return crds, nil
}
*/
