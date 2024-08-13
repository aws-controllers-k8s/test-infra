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
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// const (
// 	// defaultTokenExpirationThreshold is the default threshold to
// 	// consider a token as expired. This is used to refresh the token
// 	// before it expires.
// 	//
// 	// Maybe this too high/low? Maybe it should be configurable?
// 	defaultTokenExpirationThreshold = 5 * time.Minute
// )

// jwtToken is a helper struct to unmarshal the JWT token.
type jwtToken struct {
	Expiration int64 `json:"expiration"`

	// Unused fields omitted for brevity
	// _ string `json:"payload"`
	// _ string `json:"dataKey"`
	// _ string `json:"version"`
}

// getTokenExpirationTime returns the expiration time of the JWT token.
func getTokenExpirationTime(base64Token string) (time.Time, error) {
	var token jwtToken
	// The token is encoded in base64 and contains the expiration time
	// in the "expiration" json field. We decode the token to get the
	// expiration time.
	r := base64.NewDecoder(base64.StdEncoding, strings.NewReader(base64Token))
	if err := json.NewDecoder(r).Decode(&token); err != nil {
		return time.Time{}, err
	}
	return time.Unix(token.Expiration, 0), nil
}
