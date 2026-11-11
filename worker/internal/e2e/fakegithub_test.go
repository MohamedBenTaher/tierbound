package e2e

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeInstallationToken = "ghs_fake_installation_token" //nolint:gosec // fake token for the fake GitHub

type fakeComment struct {
	Issue string // owner/repo#number
	Body  string
	URL   string
}

// fakeGitHub implements the three GitHub endpoints the effector uses and
// records every comment. It verifies the App JWT with the public half of the
// key the harness generated, so the JWT -> installation token flow is real.
type fakeGitHub struct {
	pub            *rsa.PublicKey
	appID          string
	installationID string
	srv            *http.Server

	mu             sync.Mutex
	comments       []fakeComment
	posts          int
	postDelay      time.Duration // before storing: widens the race window
	hangAfterStore time.Duration // after storing: the client times out (timeout-after-success)
}

func startFakeGitHub(t *testing.T, h harness) *fakeGitHub {
	t.Helper()
	raw, err := os.ReadFile(h.appKeyFile) //nolint:gosec // path comes from the test harness
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("app key file is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("app key is not PKCS#1 like GitHub's: %v", err)
	}

	f := &fakeGitHub{pub: &key.PublicKey, appID: h.appID, installationID: h.installationID}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.accessToken)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/comments", f.listComments)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", f.createComment)

	ln, err := net.Listen("tcp", h.fakeAddr)
	if err != nil {
		t.Fatalf("fake GitHub listen %s: %v", h.fakeAddr, err)
	}
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	t.Cleanup(func() { _ = f.srv.Close() })
	return f
}

func (f *fakeGitHub) set(fn func(f *fakeGitHub)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// counts returns (POST requests received, comments stored) for one issue.
func (f *fakeGitHub) counts(issue string) (int, []fakeComment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeComment
	for _, c := range f.comments {
		if c.Issue == issue {
			out = append(out, c)
		}
	}
	return f.posts, out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeGitHub) accessToken(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") != f.installationID {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	if err := f.verifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": fakeInstallationToken})
}

func (f *fakeGitHub) verifyAppJWT(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("bad RS256 signature: %w", err)
	}
	rawClaims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		return err
	}
	now := time.Now().Unix()
	if claims.Iss != f.appID || claims.Exp <= now || claims.Exp-claims.Iat > 600 {
		return fmt.Errorf("bad claims %+v", claims)
	}
	return nil
}

func (f *fakeGitHub) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+fakeInstallationToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return false
	}
	return true
}

func issueKey(r *http.Request) string {
	return fmt.Sprintf("%s/%s#%s", r.PathValue("owner"), r.PathValue("repo"), r.PathValue("number"))
}

func (f *fakeGitHub) listComments(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if perPage < 1 {
		perPage = 30
	}
	if page < 1 {
		page = 1
	}
	_, all := f.counts(issueKey(r))
	out := []map[string]string{}
	for i := (page - 1) * perPage; i < len(all) && i < page*perPage; i++ {
		out = append(out, map[string]string{"body": all[i].Body, "html_url": all[i].URL})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) createComment(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	f.mu.Lock()
	f.posts++
	delay := f.postDelay
	f.mu.Unlock()
	time.Sleep(delay)

	f.mu.Lock()
	c := fakeComment{
		Issue: issueKey(r),
		Body:  in.Body,
		URL:   fmt.Sprintf("https://github.test/%s#issuecomment-%d", issueKey(r), len(f.comments)+1),
	}
	f.comments = append(f.comments, c)
	hang := f.hangAfterStore
	f.mu.Unlock()
	time.Sleep(hang)

	writeJSON(w, http.StatusCreated, map[string]string{"body": c.Body, "html_url": c.URL})
}
