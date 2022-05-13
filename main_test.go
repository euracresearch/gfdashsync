// Copyright 2022 Eurac Research. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xanzy/go-gitlab"
)

func TestGitlabAdd(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		git, _ := MustGitlab(t, http.NotFound)

		f := &File{
			UID:    "go1",
			Path:   "/dev/null.json",
			SHA256: "12345",
		}

		git.Add(f)

		if len(git.actions) != 1 {
			t.Fatal("expected only one action")
		}

		if want, got := gitlab.FileCreate, *git.actions[0].Action; want != got {
			t.Fatalf("want %v, got %v", want, got)
		}
	})

	t.Run("move", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{"go1":{"uid":"go1","path":"/dev/null.json","sha256":"12345"}}`)
		git, _ := MustGitlab(t, hf)

		f := &File{
			UID:    "go1",
			Path:   "null.json",
			SHA256: "54321",
		}

		git.Add(f)

		if len(git.history) == 0 {
			t.Fatal("expected history")
		}

		if len(git.actions) != 1 {
			t.Fatal("expected one only action")
		}

		if want, got := gitlab.FileMove, *git.actions[0].Action; want != got {
			t.Fatalf("want %v, got %v", want, got)
		}
	})

	t.Run("modified", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{"go1":{"uid":"go1","path":"/dev/null.json","sha256":"12345"}}`)
		git, _ := MustGitlab(t, hf)

		f := &File{
			UID:    "go1",
			Path:   "/dev/null.json",
			SHA256: "54321",
		}

		git.Add(f)

		if len(git.history) == 0 {
			t.Fatal("expected history")
		}

		if len(git.actions) != 1 {
			t.Fatal("expected one only action")
		}

		if want, got := gitlab.FileUpdate, *git.actions[0].Action; want != got {
			t.Fatalf("want %v, got %v", want, got)
		}
	})

	t.Run("nothing", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{"go1":{"uid":"go1","path":"/dev/null.json","sha256":"12345"}}`)
		git, _ := MustGitlab(t, hf)

		f := &File{
			UID:    "go1",
			Path:   "/dev/null.json",
			SHA256: "12345",
		}

		git.Add(f)

		if len(git.history) == 0 {
			t.Fatal("expected history")
		}

		if len(git.actions) != 0 {
			t.Fatal("expected no action")
		}
	})

}

func TestGitlabCommit(t *testing.T) {
	t.Run("allEmpty", func(t *testing.T) {
		git, _ := MustGitlab(t, http.NotFound)
		if err := git.Commit(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("noChangesNoCommit", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{
			"go1": {
				"uid": "go1",
				"path": "/dev/null.json",
				"sha256": "12345"
			}
		}`)

		git, _ := MustGitlab(t, hf)

		f := &File{
			UID:    "go1",
			Path:   "/dev/null.json",
			SHA256: "12345",
		}

		git.Add(f)

		if len(git.actions) != 0 {
			t.Fatal("expected no action")
		}

		if err := git.Commit(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("oneFileChangeTwoActions", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{
			"go1": {
				"uid": "go1",
				"path": "/dev/null.json",
				"sha256": "12345"
			}
		}`)

		git, mux := MustGitlab(t, hf)
		mux.HandleFunc("/api/v4/projects/1/", commitHandler(t, http.StatusOK))

		f := &File{
			UID:    "go1",
			Path:   "/dev/null.json",
			SHA256: "54321",
		}

		git.Add(f)

		if err := git.Commit(); err != nil {
			t.Fatal(err)
		}

		if len(git.actions) != 2 {
			t.Fatal("expected two action, one for the file one for the history.")
		}
	})

	t.Run("multipleChanges", func(t *testing.T) {
		hf := MustHistoryHandler(t, `{
			"1": {
				"uid": "1",
				"path": "/dev/null1.json",
				"sha256": "12345"
			}
		}`)

		git, mux := MustGitlab(t, hf)
		mux.HandleFunc("/api/v4/projects/1/", commitHandler(t, http.StatusOK))

		inSize := 4
		for i := 1; i <= inSize; i++ {
			f := &File{
				UID:    fmt.Sprintf("%d", i),
				Path:   fmt.Sprintf("/dev/null%d.json", i),
				SHA256: "54321",
			}

			git.Add(f)
		}

		if err := git.Commit(); err != nil {
			t.Fatal(err)
		}

		if len(git.actions) != (inSize + 1) {
			t.Fatal("expected five actions (4 files, 1 history).")
		}
	})
}

func commitHandler(t *testing.T, status int) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte("{}"))
	}
}

func MustHistoryHandler(t *testing.T, in string) http.HandlerFunc {
	t.Helper()

	gf := &gitlab.File{
		FileName: "history.json",
		Content:  base64.StdEncoding.EncodeToString([]byte(in)),
	}

	j, err := json.Marshal(gf)
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Write(j)
	}
}

func MustGitlab(t *testing.T, historyHandler http.HandlerFunc) (*Gitlab, *http.ServeMux) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/1/repository/files/", historyHandler)

	server := httptest.NewServer(mux)

	gl, err := NewGitlab(server.URL, "", "test", 1)
	if err != nil {
		t.Fatal(err)
	}

	return gl, mux
}
