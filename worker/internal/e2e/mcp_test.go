package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// signedInUser creates a confirmed local Auth user and signs in with a
// password grant. The access token is an ordinary user JWT (ES256 via the
// local signing key), which the mcp function's withSupabase({auth: "user"})
// accepts like the token Cursor gets from the OAuth flow.
func (h harness) signedInUser(t *testing.T) (string, string) {
	t.Helper()
	email := fmt.Sprintf("e2e-%d@tierbound.test", time.Now().UnixNano())
	password := fmt.Sprintf("pw-%d", time.Now().UnixNano())
	creds, err := json.Marshal(map[string]any{"email": email, "password": password, "email_confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	admin := map[string]string{"apikey": h.serviceKey, "Authorization": "Bearer " + h.serviceKey}
	status, raw, err := h.request(http.MethodPost, "/auth/v1/admin/users", admin, creds)
	if err != nil || status != http.StatusOK {
		t.Fatalf("create user: %d %s %v", status, raw, err)
	}
	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.cleanupOwner(user.ID) })

	status, raw, err = h.request(http.MethodPost, "/auth/v1/token?grant_type=password",
		map[string]string{"apikey": h.anonKey}, creds)
	if err != nil || status != http.StatusOK {
		t.Fatalf("sign in: %d %s %v", status, raw, err)
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	return user.ID, session.AccessToken
}

type toolResult struct {
	IsError bool
	Text    string
}

// callTool sends one JSON-RPC tools/call to the mcp function (Streamable
// HTTP; the response arrives as a single SSE message).
func (h harness) callTool(t *testing.T, token, tool string, args map[string]any) toolResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, raw, err := h.request(http.MethodPost, "/functions/v1/mcp", map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json, text/event-stream",
	}, body)
	if err != nil || status != http.StatusOK {
		t.Fatalf("mcp %s: %d %s %v", tool, status, raw, err)
	}
	payload := string(raw)
	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	var rpc struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
		t.Fatalf("mcp %s: unparseable response %q: %v", tool, raw, err)
	}
	if rpc.Error != nil {
		t.Fatalf("mcp %s: JSON-RPC error %s", tool, rpc.Error.Message)
	}
	var text strings.Builder
	for _, c := range rpc.Result.Content {
		text.WriteString(c.Text)
	}
	return toolResult{IsError: rpc.Result.IsError, Text: text.String()}
}
