// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package verification

import (
	"context"
	"errors"
	"fmt"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/exposure-notifications-server/internal/verification/database"
	"github.com/google/exposure-notifications-server/internal/verification/model"
	"github.com/google/exposure-notifications-server/pkg/logging"
)

// AuthenticateStatsToken parse the provided JWT and determines if it is an authorized stats request.
func (v *Verifier) AuthenticateStatsToken(ctx context.Context, rawToken string) (int64, error) {
	logger := logging.FromContext(ctx)

	var healthAuthorityID int64
	var claims *jwt.StandardClaims

	token, err := jwt.ParseWithClaims(rawToken, &jwt.StandardClaims{}, func(token *jwt.Token) (interface{}, error) {
		if method, ok := token.Method.(*jwt.SigningMethodECDSA); !ok || method.Name != jwt.SigningMethodES256.Name {
			return nil, fmt.Errorf("unsupported signing method, must be %v", jwt.SigningMethodES256.Name)
		}

		kidHeader, ok := token.Header["kid"]
		if !ok {
			err := errors.New("missing 'kid' header in token")
			logger.Infow("invalid authentication token", "error", err)
			return nil, err
		}

		kid, ok := kidHeader.(string)
		if !ok {
			err := errors.New("invalid 'kid' field in token")
			logger.Infow("invalid authentication token", "error", err)
			return nil, err
		}

		claims, ok = token.Claims.(*jwt.StandardClaims)
		if !ok {
			return nil, fmt.Errorf("token does not contain expected claim set")
		}

		lookup := func() (interface{}, error) {
			// Based on issuer, load the key versions.
			ha, err := v.db.GetHealthAuthority(ctx, claims.Issuer)
			// Special case not found so that we can cache it.
			if errors.Is(err, database.ErrHealthAuthorityNotFound) {
				logger.Warnw("requested issuer not found", "iss", claims.Issuer)
				return nil, nil
			}
			if err != nil {
				return nil, fmt.Errorf("error looking up issuer: %v : %w", claims.Issuer, err)
			}
			return ha, nil
		}
		cacheVal, err := v.haCache.WriteThruLookup(claims.Issuer, lookup)
		if err != nil {
			return nil, err
		}

		if cacheVal == nil {
			return nil, fmt.Errorf("issuer not found: %v", claims.Issuer)
		}

		healthAuthority := cacheVal.(*model.HealthAuthority)
		// Look for the matching 'kid'
		for _, key := range healthAuthority.Keys {
			if key.Version == kid && key.IsValid() {
				healthAuthorityID = healthAuthority.ID
				return key.PublicKey()
			}
		}
		return nil, fmt.Errorf("key not found: kid: %v iss: %v ", kid, claims.Issuer)
	})
	if err != nil {
		return 0, fmt.Errorf("unauthorized: %w", err)
	}

	if !token.Valid {
		return 0, fmt.Errorf("authentication token invalid")
	}

	if !claims.VerifyAudience(v.config.StatsAudience, true) {
		logger.Warnw("stats audience mismatch", "expected", v.config.StatsAudience, "got", claims.Audience)
		return 0, fmt.Errorf("unauthorized, audience mismatch")
	}

	return healthAuthorityID, nil
}
