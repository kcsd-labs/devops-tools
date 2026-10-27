package api

import (
	"context"
	"testing"

	"devops-tools/internal/configsource"
)

// repo is a configuration source holding one file. It keeps no history of its
// own; withHistory adds that.
type repo struct {
	content, commit string
}

func (r *repo) Kind() string                                       { return "git" }
func (r *repo) Root() string                                       { return "team/configs@dev" }
func (r *repo) List(context.Context) ([]configsource.Entry, error) { return nil, nil }
func (r *repo) Get(_ context.Context, p string) (configsource.File, error) {
	return configsource.File{Entry: configsource.Entry{Path: p, Version: r.commit}, Content: r.content}, nil
}
func (r *repo) Put(context.Context, string, string, string, configsource.Editor) error { return nil }

// withHistory adds what Git has and Consul has not.
type withHistory struct{ *repo }

func (h withHistory) RevisionURL(v string) string {
	return "https://gitlab.example.com/team/configs/-/commit/" + v
}

func TestTheRecordLinksTheCommitTheSaveMade(t *testing.T) {
	s := &Server{configs: withHistory{&repo{content: "a: 2\n", commit: "abc123"}}}
	got := s.commitURL(context.Background(), "x.yaml", "a: 2")
	if got != "https://gitlab.example.com/team/configs/-/commit/abc123" {
		t.Errorf("commitURL = %q", got)
	}
}

// Somebody else committed between the save and the read-back: the file no
// longer holds what was written, so its last commit is not this save's, and
// naming it would point a reviewer at the wrong change.
func TestSomebodyElsesCommitIsNotPassedOffAsThisOne(t *testing.T) {
	s := &Server{configs: withHistory{&repo{content: "a: 3\n", commit: "def456"}}}
	if got := s.commitURL(context.Background(), "x.yaml", "a: 2\n"); got != "" {
		t.Errorf("commitURL = %q, want none", got)
	}
}

func TestASourceWithoutHistoryIsNotEvenRead(t *testing.T) {
	s := &Server{configs: &repo{content: "a: 2\n", commit: "17"}}
	if got := s.commitURL(context.Background(), "x.yaml", "a: 2\n"); got != "" {
		t.Errorf("commitURL = %q, want none", got)
	}
}
