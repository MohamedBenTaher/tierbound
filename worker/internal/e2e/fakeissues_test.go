package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// fakeLabels are the labels every fake repo has (V2_ACTIONS 4.1: the
// effector refuses labels the repo does not have).
var fakeLabels = []string{"bug", "effector", "test"}

type fakeIssue struct {
	Repo    string
	Title   string
	Body    string
	Labels  []string
	URL     string
	Updated time.Time
}

func repoKey(r *http.Request) string {
	return r.PathValue("owner") + "/" + r.PathValue("repo")
}

// issuesIn returns (issue POSTs received, issues stored) for one repo.
func (f *fakeGitHub) issuesIn(repo string) (int, []fakeIssue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeIssue
	for _, i := range f.issues {
		if i.Repo == repo {
			out = append(out, i)
		}
	}
	return f.issuePosts, out
}

func (f *fakeGitHub) listLabels(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	out := []map[string]string{}
	for _, l := range fakeLabels {
		out = append(out, map[string]string{"name": l})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) listIssues(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	q := r.URL.Query()
	since, err := time.Parse(time.RFC3339, q.Get("since"))
	if err != nil || q.Get("state") != "all" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "want state=all and an RFC 3339 since"})
		return
	}
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, _ := strconv.Atoi(q.Get("page"))
	_, all := f.issuesIn(repoKey(r))
	all = slices.DeleteFunc(all, func(i fakeIssue) bool { return i.Updated.Before(since) })
	out := []map[string]string{}
	for i := (max(page, 1) - 1) * perPage; i < len(all) && i < max(page, 1)*perPage; i++ {
		out = append(out, map[string]string{"body": all[i].Body, "html_url": all[i].URL})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) createIssue(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var in struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	f.mu.Lock()
	f.issuePosts++
	delay, fail := f.postDelay, f.failPosts
	f.mu.Unlock()
	if fail {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Server Error"})
		return
	}
	time.Sleep(delay)

	f.mu.Lock()
	issue := fakeIssue{
		Repo: repoKey(r), Title: in.Title, Body: in.Body, Labels: in.Labels, Updated: time.Now(),
		URL: fmt.Sprintf("https://github.test/%s/issues/%d", repoKey(r), 1000+len(f.issues)),
	}
	f.issues = append(f.issues, issue)
	hang := f.hangAfterStore
	f.mu.Unlock()
	time.Sleep(hang)

	writeJSON(w, http.StatusCreated, map[string]string{"html_url": issue.URL})
}
