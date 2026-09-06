package integration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// sigv4ReferenceCheck re-implements the AWS SigV4 verification procedure
// (written from the published algorithm, independently of the objectstore
// client code) and reports whether the request's Authorization header
// carries a valid signature for secret.
func sigv4ReferenceCheck(r *http.Request, authorization, secret string) bool {
	const prefix = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	var credential, signedHeaders, gotSignature string
	for _, part := range strings.Split(strings.TrimPrefix(authorization, prefix), ", ") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return false
		}
		switch kv[0] {
		case "Credential":
			credential = kv[1]
		case "SignedHeaders":
			signedHeaders = kv[1]
		case "Signature":
			gotSignature = kv[1]
		}
	}
	credParts := strings.Split(credential, "/")
	if len(credParts) != 5 {
		return false
	}
	// Credential = <access-key>/<date>/<region>/<service>/<terminal>.
	dateStamp, region, service, terminal := credParts[1], credParts[2], credParts[3], credParts[4]

	// Canonical headers, exactly the signed list.
	names := strings.Split(signedHeaders, ";")
	sort.Strings(names)
	var canon strings.Builder
	for _, name := range names {
		canon.WriteString(name)
		canon.WriteString(":")
		// Go keeps the Host outside r.Header on the server side.
		value := r.Host
		if name != "host" {
			value = r.Header.Get(name)
		}
		canon.WriteString(strings.TrimSpace(value))
		canon.WriteString("\n")
	}

	payloadHash := r.Header.Get("x-amz-content-sha256")
	canonicalRequest := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		r.URL.RawQuery,
		canon.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	amzDate := r.Header.Get("x-amz-date")
	scope := dateStamp + "/" + region + "/" + service + "/" + terminal
	hashed := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(hashed[:])}, "\n")

	// Derive the signing key per the AWS algorithm.
	kDate := hmacSHA256Ref([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256Ref(kDate, []byte(region))
	kService := hmacSHA256Ref(kRegion, []byte(service))
	kSigning := hmacSHA256Ref(kService, []byte(terminal))
	sig := hmacSHA256Ref(kSigning, []byte(stringToSign))
	return fmt.Sprintf("%x", sig) == gotSignature
}

func hmacSHA256Ref(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
