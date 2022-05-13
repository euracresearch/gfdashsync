// Copyright 2022 Eurac Research. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// gfdashsync is a command for syncing all Grafana dashboards to a Gitlab
// repository.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	gapi "github.com/grafana/grafana-api-golang-client"
	"github.com/xanzy/go-gitlab"
)

func main() {
	var (
		gfAPI     = flag.String("grafana.api", "", "Grafana API URL")
		gfToken   = flag.String("grafana.token", "", "Grafana API token")
		gitAPI    = flag.String("git.api", "", "Git service API URL")
		gitToken  = flag.String("git.token", "", "Git service API token")
		gitPID    = flag.Int("git.pid", 0, "Git project ID")
		gitBranch = flag.String("git.branch", "main", "Git repository branch")
		config    = flag.String("config", "config", "Config file (optional)")
	)
	flag.Parse()

	if err := setFlagsFromFile(*config); err != nil {
		log.Fatal(err)
	}

	gf, err := gapi.New(*gfAPI, gapi.Config{APIKey: *gfToken})
	if err != nil {
		log.Fatalf("failed to create grafana client: %v", err)
	}

	git, err := NewGitlab(*gitAPI, *gitToken, *gitBranch, *gitPID)
	if err != nil {
		log.Fatal(err)
	}

	dashboards, err := gf.Dashboards()
	if err != nil {
		log.Fatal(err)
	}

	for _, d := range dashboards {
		b, err := gf.DashboardByUID(d.UID)
		if err != nil {
			log.Printf("error getting dashboard %q with ID %d", d.Title, d.ID)
			continue
		}

		data, err := json.MarshalIndent(b, "", "	")
		if err != nil {
			log.Printf("error converting to JSON of dashboard %q with ID %d", d.Title, d.ID)
			continue
		}

		f := &File{
			UID:     d.UID,
			Path:    fmt.Sprintf("/%s/%s.json", d.FolderTitle, d.Title),
			SHA256:  hash(data),
			content: data,
		}

		git.Add(f)
	}

	if err := git.Commit(); err != nil {
		log.Fatal(err)
	}
}

func hash(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

type History map[string]*File

type File struct {
	UID    string `json:"uid"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`

	content   []byte
	processed bool
}

func (f *File) moved(hf *File) bool {
	return (f.Path != hf.Path) && (f.UID == hf.UID) && (f.SHA256 != hf.SHA256)
}

func (f *File) modified(hf *File) bool {
	return (f.Path == hf.Path) && (f.UID == hf.UID) && (f.SHA256 != hf.SHA256)
}

type Gitlab struct {
	client *gitlab.Client
	pid    int
	branch string

	history       History
	historyFile   string
	historyAction gitlab.FileActionValue

	actions []*gitlab.CommitActionOptions
}

func NewGitlab(baseURL, token, branch string, pid int) (*Gitlab, error) {
	c, err := gitlab.NewClient(token, gitlab.WithBaseURL(baseURL))
	if err != nil {
		return nil, fmt.Errorf("gitlab: error creating client: %w", err)
	}

	g := &Gitlab{
		client:        c,
		pid:           pid,
		branch:        branch,
		history:       make(History),
		historyFile:   "history.json",
		historyAction: gitlab.FileUpdate,
	}

	if err := g.parseHistory(); err != nil {
		return nil, fmt.Errorf("gitlab: error parsing history: %w", err)
	}

	return g, nil
}

// parseHistory reads "history.json" from the repository.
func (g *Gitlab) parseHistory() error {
	f, resp, err := g.client.RepositoryFiles.GetFile(g.pid, g.historyFile, &gitlab.GetFileOptions{
		Ref: gitlab.String(g.branch),
	}, nil)
	if err != nil {
		// If the error is a 404 File Not Found we will assume there is no
		// history and processed without error but setting the action to create
		// a new history file. All other errors will be returned as such.
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			g.historyAction = gitlab.FileCreate
			return nil
		}

		return err
	}

	data, err := base64.StdEncoding.DecodeString(f.Content)
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(data), &g.history)
}

// Add adds the file to be committed.
func (g *Gitlab) Add(in *File) {
	hf, ok := g.history[in.UID]
	if !ok {
		g.add(in, gitlab.FileCreate, "")
		return
	}

	switch {
	case in.moved(hf):
		g.add(in, gitlab.FileMove, hf.Path)

	case in.modified(hf):
		g.add(in, gitlab.FileUpdate, "")

	default:
		// If no action is preformed set the processed flag anyway.
		hf.processed = true
	}
}

func (g *Gitlab) add(in *File, action gitlab.FileActionValue, prevPath string) {
	in.processed = true

	opt := &gitlab.CommitActionOptions{
		Action:   gitlab.FileAction(action),
		FilePath: gitlab.String(in.Path),
		Content:  gitlab.String(string(in.content)),
	}

	if prevPath != "" {
		opt.PreviousPath = gitlab.String(prevPath)
	}

	g.actions = append(g.actions, opt)
	g.history[in.UID] = in
}

func (g *Gitlab) updateHistory() error {
	if len(g.history) == 0 {
		return nil
	}

	data, err := json.Marshal(g.history)
	if err != nil {
		return err
	}

	opt := &gitlab.CommitActionOptions{
		Action:   gitlab.FileAction(g.historyAction),
		FilePath: gitlab.String(g.historyFile),
		Content:  gitlab.String(string(data)),
	}

	g.actions = append(g.actions, opt)
	return nil
}

func (g *Gitlab) deleteOrphans() {
	for _, f := range g.history {
		if f.processed {
			continue
		}

		g.actions = append(g.actions, &gitlab.CommitActionOptions{
			Action:   gitlab.FileAction(gitlab.FileDelete),
			FilePath: gitlab.String(f.Path),
		})

		delete(g.history, f.UID)
	}
}

// Commit commits all pending commits to the repository.
func (g *Gitlab) Commit() error {
	g.deleteOrphans()

	// nothing to commit
	if len(g.actions) == 0 {
		return nil
	}

	if err := g.updateHistory(); err != nil {
		return err
	}

	opt := &gitlab.CreateCommitOptions{
		Branch:        gitlab.String(g.branch),
		CommitMessage: gitlab.String("ʕ◔ϖ◔ʔ: backup done."),
		Actions:       g.actions,
	}
	_, _, err := g.client.Commits.CreateCommit(g.pid, opt, nil)
	if err != nil {
		return fmt.Errorf("gitlab: commit error: %w", err)
	}

	return nil
}

func setFlagsFromFile(filename string) error {
	c, err := os.Open(filename)
	if err != nil {
		return err
	}

	flag.VisitAll(func(f *flag.Flag) {
		s := bufio.NewScanner(c)
		for s.Scan() {
			f := strings.Fields(s.Text())

			if len(f) != 2 {
				continue
			}
			flag.Set(f[0], f[1])
		}
	})

	return nil
}
