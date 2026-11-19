package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type fakeReview struct {
	PR       string // owner/repo#number
	CommitID string
	Event    string
	Body     string
	Comments []map[string]any
	URL      string
}

type fakeStatus struct {
	Repo, SHA, State, Context, Description string
}

func prKey(r *http.Request) string {
	return fmt.Sprintf("%s#%s", repoKey(r), r.PathValue("number"))
}

// setHead sets the current head sha of a fake PR (owner/repo#number).
func (f *fakeGitHub) setHead(pr, sha string) {
	f.set(func(f *fakeGitHub) {
		if f.heads == nil {
			f.heads = map[string]string{}
		}
		f.heads[pr] = sha
	})
}

// reviewsOn returns (review POSTs received, reviews stored, statuses stored).
func (f *fakeGitHub) reviewsOn(pr, repo string) (int, []fakeReview, []fakeStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rs []fakeReview
	for _, r := range f.reviews {
		if r.PR == pr {
			rs = append(rs, r)
		}
	}
	var ss []fakeStatus
	for _, s := range f.statuses {
		if s.Repo == repo {
			ss = append(ss, s)
		}
	}
	return f.reviewPosts, rs, ss
}

func (f *fakeGitHub) getPull(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	f.mu.Lock()
	sha, ok := f.heads[prKey(r)]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"head": map[string]string{"sha": sha}})
}

func (f *fakeGitHub) listReviews(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	_, all, _ := f.reviewsOn(prKey(r), "")
	out := []map[string]string{}
	for i := (max(page, 1) - 1) * perPage; i < len(all) && i < max(page, 1)*perPage; i++ {
		out = append(out, map[string]string{"body": all[i].Body, "html_url": all[i].URL})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeGitHub) createReview(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var in struct {
		CommitID string           `json:"commit_id"`
		Event    string           `json:"event"`
		Body     string           `json:"body"`
		Comments []map[string]any `json:"comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	f.mu.Lock()
	f.reviewPosts++
	delay, forbid := f.postDelay, f.forbidReviews
	f.mu.Unlock()
	if forbid {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
		return
	}
	time.Sleep(delay)

	f.mu.Lock()
	rv := fakeReview{
		PR: prKey(r), CommitID: in.CommitID, Event: in.Event, Body: in.Body, Comments: in.Comments,
		URL: fmt.Sprintf("https://github.test/%s/pull/%s#pullrequestreview-%d", repoKey(r), r.PathValue("number"), len(f.reviews)+1),
	}
	f.reviews = append(f.reviews, rv)
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"html_url": rv.URL})
}

func (f *fakeGitHub) createStatus(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var in struct {
		State, Context, Description string
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	f.mu.Lock()
	fail := f.failStatuses
	if !fail {
		f.statuses = append(f.statuses, fakeStatus{Repo: repoKey(r), SHA: r.PathValue("sha"), State: in.State, Context: in.Context, Description: in.Description})
	}
	f.mu.Unlock()
	if fail {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Server Error"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"state": in.State})
}
