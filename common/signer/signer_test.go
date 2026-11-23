package signer

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	testAccessKey = "AKTESTEXAMPLE"
	testSecretKey = "SKTESTEXAMPLE"
	testDate      = "20150629T012459Z"
)

// expectedAuthValue computes the Authorization header value for the given
// canonical request following the documented Huawei Cloud API signing
// algorithm (SDK-HMAC-SHA256), independently from the Signer implementation.
func expectedAuthValue(canonicalRequest, signedHeaders string) string {
	stringToSign := fmt.Sprintf("SDK-HMAC-SHA256\n%s\n%x",
		testDate, sha256.Sum256([]byte(canonicalRequest)))

	mac := hmac.New(sha256.New, []byte(testSecretKey))
	mac.Write([]byte(stringToSign))

	return fmt.Sprintf("SDK-HMAC-SHA256 Access=%s, SignedHeaders=%s, Signature=%x",
		testAccessKey, signedHeaders, mac.Sum(nil))
}

func TestSignerSignPOST(t *testing.T) {
	body := `{"kind":"Deployment"}`
	req, err := http.NewRequest(http.MethodPost,
		"https://cce.example.com/apis/apps/v1/namespaces/default/deployments?fieldSelector=x%20y",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// A fixed date keeps the signature deterministic.
	req.Header.Set(HeaderXDateTime, testDate)

	s := &Signer{Key: testAccessKey, Secret: testSecretKey}
	if err := s.Sign(req); err != nil {
		t.Fatalf("unexpected error signing request: %s", err)
	}

	if got := req.Header.Get(HeaderXDateTime); got != testDate {
		t.Errorf("expected %s header to be preserved, got %q", HeaderXDateTime, got)
	}

	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/apis/apps/v1/namespaces/default/deployments/", // CanonicalURI appends a trailing slash
		"fieldSelector=x%20y",
		"content-type:application/json\nx-sdk-date:" + testDate + "\n",
		"content-type;x-sdk-date",
		fmt.Sprintf("%x", sha256.Sum256([]byte(body))),
	}, "\n")

	if want := expectedAuthValue(canonicalRequest, "content-type;x-sdk-date"); req.Header.Get(HeaderXAuthorization) != want {
		t.Errorf("unexpected Authorization header:\n got: %s\nwant: %s", req.Header.Get(HeaderXAuthorization), want)
	}

	// The signer must leave a readable, intact request body behind.
	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != body {
		t.Errorf("expected body to be restored after signing, got %q", gotBody)
	}
}

func TestSignerSignGETNoBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://cce.example.com/api/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderXDateTime, testDate)

	s := &Signer{Key: testAccessKey, Secret: testSecretKey}
	if err := s.Sign(req); err != nil {
		t.Fatalf("unexpected error signing request: %s", err)
	}

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		"/api/",
		"", // no query string
		"x-sdk-date:" + testDate + "\n",
		"x-sdk-date",
		fmt.Sprintf("%x", sha256.Sum256([]byte(""))),
	}, "\n")

	if want := expectedAuthValue(canonicalRequest, "x-sdk-date"); req.Header.Get(HeaderXAuthorization) != want {
		t.Errorf("unexpected Authorization header:\n got: %s\nwant: %s", req.Header.Get(HeaderXAuthorization), want)
	}
}

func TestSignerSignIsStable(t *testing.T) {
	// Signing the same request twice with a pinned date must produce the
	// same signature.
	makeReq := func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, "https://cce.example.com/version", nil)
		req.Header.Set(HeaderXDateTime, testDate)
		return req
	}

	s := &Signer{Key: testAccessKey, Secret: testSecretKey}
	if err := s.Sign(makeReq()); err != nil {
		t.Fatal(err)
	}
	first := makeReq()
	if err := s.Sign(first); err != nil {
		t.Fatal(err)
	}
	second := makeReq()
	if err := s.Sign(second); err != nil {
		t.Fatal(err)
	}

	if first.Header.Get(HeaderXAuthorization) != second.Header.Get(HeaderXAuthorization) {
		t.Error("expected signing to be deterministic for identical requests")
	}
}
