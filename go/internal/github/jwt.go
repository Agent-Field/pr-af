package github

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// generateAppJWT builds the short-lived RS256 JWT used to authenticate as a
// GitHub App. It is a byte-exact port of _generate_app_jwt in
// src/pr_af/github/client.py:
//
//	iat = now - 60   (60s clock-skew buffer)
//	exp = now + 600  (10 minutes, the maximum GitHub allows)
//	iss = app id     (the raw GITHUB_APP_ID string)
//
// The private key PEM (PKCS#1 or PKCS#8) is parsed on each call; a parse
// failure surfaces as an error at token-generation time, mirroring Python's
// jwt.encode raising on a bad key.
func generateAppJWT(appID, privateKeyPEM string) (string, error) {
	now := time.Now().Unix()
	claims := jwt.MapClaims{
		"iat": now - 60,
		"exp": now + (10 * 60),
		"iss": appID,
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", err
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return tok.SignedString(key)
}
