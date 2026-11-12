package e2e

import (
	"encoding/json"
	"testing"
)

// Plan "Setup smoothing" B2: the before-user-created hook admits only
// allowlisted GitHub ids, and only the Auth server may call it.
func TestApproverHookAdmitsOnlyAllowlistedGitHubIDs(t *testing.T) {
	h := setup(t)
	const allowed = 990000000123
	h.exec(t, `insert into public.tierbound_approvers (github_user_id, github_login) values ($1, 'test-approver')
		on conflict do nothing`, allowed)
	t.Cleanup(func() { h.exec(t, `delete from public.tierbound_approvers where github_user_id = $1`, allowed) })

	event := func(provider, githubID string) string {
		b, _ := json.Marshal(map[string]any{"user": map[string]any{
			"app_metadata":  map[string]any{"provider": provider},
			"user_metadata": map[string]any{"provider_id": githubID, "sub": githubID},
		}})
		return string(b)
	}
	cases := []struct {
		name, event string
		admit       bool
	}{
		{"allowlisted github id", event("github", "990000000123"), true},
		{"unknown github id", event("github", "990000000999"), false},
		{"email sign-up", event("email", ""), false},
		{"allowlisted id from another provider", event("gitlab", "990000000123"), false},
		{"non-numeric id", event("github", "1 or 1=1"), false},
	}
	for _, c := range cases {
		var out string
		if err := h.db.QueryRow(t.Context(),
			`select public.tierbound_before_user_created($1::jsonb)::text`, c.event).Scan(&out); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if admitted := out == "{}"; admitted != c.admit {
			t.Errorf("%s: admit=%v, got %s", c.name, c.admit, out)
		}
	}

	// authenticated (any signed-in user) can neither call the hook nor read the list.
	tx, err := h.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `set local role authenticated`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `select public.tierbound_before_user_created('{}'::jsonb)`); err == nil {
		t.Fatal("authenticated executed the hook")
	}
}
