// Package issuedraft is the one definition of what an issue-proposing agent
// may reply (plan steps 4.6, 4.7): the worker proposes only drafts that pass
// Parse, and the eval gate scores with the same Parse, so what the gate
// accepts is exactly what the worker would propose.
//
// A reply is one strict JSON object, either
//
//	{"title": "...", "body": "...", "labels": ["..."]}   propose one issue
//	{"abstain": "<one-line reason>"}                     propose nothing
package issuedraft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits (V2_ACTIONS 4.1). The effector re-checks the payload, and the
// labels against the repo, before it posts.
const (
	MaxTitle   = 80
	MaxLabels  = 10
	MaxAbstain = 200
)

// Draft is a parsed reply. Exactly one of Abstain or {Title, Body} is set.
type Draft struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Labels  []string `json:"labels"`
	Abstain string   `json:"abstain"`
}

// Abstained reports whether the agent declined to propose an issue.
func (d Draft) Abstained() bool { return d.Abstain != "" }

// markerPrefix is the effector's dedupe marker (SPEC section 7). A draft
// that carries one could make a retry "find" a post that never happened.
const markerPrefix = "<!-- tierbound:"

// Parse strictly decodes and validates a reply: one JSON object, no unknown
// fields, no trailing content. Anything else is an error; the worker never
// proposes it (the job retries, then dead-letters).
func Parse(s string) (Draft, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(s))))
	dec.DisallowUnknownFields()
	var d Draft
	if err := dec.Decode(&d); err != nil {
		return Draft{}, fmt.Errorf("issue draft is not the expected JSON object: %w", err)
	}
	if dec.More() {
		return Draft{}, errors.New("issue draft has trailing content after the JSON object")
	}
	d.Title = strings.TrimSpace(d.Title)
	d.Body = strings.TrimSpace(d.Body)
	d.Abstain = strings.TrimSpace(d.Abstain)
	if d.Labels == nil {
		d.Labels = []string{}
	}
	if d.Abstained() {
		return d, d.validateAbstain()
	}
	return d, d.validateIssue()
}

func (d Draft) validateAbstain() error {
	switch {
	case d.Title != "" || d.Body != "" || len(d.Labels) > 0:
		return errors.New("issue draft abstains and proposes at once")
	case utf8.RuneCountInString(d.Abstain) > MaxAbstain || strings.ContainsAny(d.Abstain, "\r\n"):
		return fmt.Errorf("abstain reason must be one line of at most %d characters", MaxAbstain)
	}
	return nil
}

func (d Draft) validateIssue() error {
	n := utf8.RuneCountInString(d.Title)
	switch {
	case n == 0 || n > MaxTitle || strings.ContainsAny(d.Title, "\r\n"):
		return fmt.Errorf("issue draft title must be one line of 1-%d characters", MaxTitle)
	case d.Body == "":
		return errors.New("issue draft body is empty")
	case strings.Contains(d.Title+d.Body, markerPrefix):
		return errors.New("issue draft contains a tierbound marker")
	case len(d.Labels) > MaxLabels:
		return fmt.Errorf("issue draft has more than %d labels", MaxLabels)
	}
	for _, l := range d.Labels {
		if strings.TrimSpace(l) == "" {
			return errors.New("issue draft has an empty label")
		}
	}
	return nil
}
