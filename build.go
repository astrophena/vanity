//usr/bin/env go run $0 $@; exit $?

// © 2023 Ilya Mateyko. All rights reserved.
// Use of this source code is governed by the ISC
// license that can be found in the LICENSE file.

//go:build ignore

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	//go:embed templates/index.html
	index     string
	indexTmpl = template.Must(template.New("index").Funcs(tplFuncs).Parse(index))
	//go:embed templates/import.html
	importP    string
	importTmpl = template.Must(template.New("import").Funcs(tplFuncs).Parse(importP))

	rev      string
	tplFuncs = template.FuncMap{
		"rev": func() string { return rev },
	}
)

func main() {
	log.SetFlags(0)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ./build.go [flags] [dir]\n")
	}
	flag.Parse()

	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); os.IsNotExist(err) {
		log.Fatal("Are you at repo root?")
	} else if err != nil {
		log.Fatal(err)
	}

	dir := filepath.Join(".", "build")
	if len(flag.Args()) > 0 {
		dir = flag.Args()[0]
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("set GITHUB_TOKEN environment variable")
	}

	revb, err := exec.Command("git", "rev-parse", "--short", "HEAD").CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}
	rev = strings.TrimSpace(string(revb))

	if err := build(dir, rev, token); err != nil {
		log.Fatal(err)
	}
}

const userReposURL = "https://api.github.com/users/astrophena/repos"

func build(dir, rev, token string) error {
	// Clean up after previous build.
	if _, err := os.Stat(dir); err == nil {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Obtain needed repositories from GitHub API.
	allRepos, err := doJSONRequest[[]*repo](http.MethodGet, userReposURL, token, http.StatusOK)
	if err != nil {
		return err
	}

	var repos []*repo
	for _, repo := range allRepos {
		if repo.Private || repo.Name == "vanity" {
			continue
		}

		files, err := doJSONRequest[[]file](http.MethodGet, repo.URL+"/contents", token, http.StatusOK)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.Path == "go.mod" {
				repos = append(repos, repo)
				break
			}
		}
	}

	// Some modifications.
	for _, repo := range repos {
		if !strings.HasSuffix(repo.Description, ".") {
			repo.Description += "."
		}
	}

	tmpdir, err := os.MkdirTemp("", "vanity")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)

	for _, repo := range repos {
		if err := exec.Command("git", "clone", repo.CloneURL, filepath.Join(tmpdir, repo.Name)).Run(); err != nil {
			return err
		}
		var obuf, errbuf bytes.Buffer
		list := exec.Command("go", "list", "-json", "./...")
		list.Dir = filepath.Join(tmpdir, repo.Name)
		list.Stdout = &obuf
		list.Stderr = &errbuf
		if err := list.Run(); err != nil {
			return fmt.Errorf("go list failed for repo %s: %v (it returned %q)", repo.Name, err, errbuf.String())
		}

		dec := json.NewDecoder(&obuf)
		for dec.More() {
			p := new(pkg)
			if err := dec.Decode(p); err != nil {
				return err
			}
			repo.Pkgs = append(repo.Pkgs, p)
		}

	}

	// Build index page.
	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, repos); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Build repo pages.
	for _, repo := range repos {
		buf.Reset()

		if err := importTmpl.Execute(&buf, repo); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, repo.Name+".html"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}

	return nil
}

type repo struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	CloneURL    string `json:"clone_url"`
	Pkgs        []*pkg // Go packages that this repo contains
}

// bits of `go list -json` that we need.
type pkg struct {
	ImportPath string   // import path of package in dir
	Doc        string   // package documentation string
	GoFiles    []string // .go source files
	Imports    []string // import paths used by this package
}

type file struct{ Path string }

func doJSONRequest[R any](method, url, token string, wantStatus int) (R, error) {
	var resp R

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return resp, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	defer res.Body.Close()

	b, err := io.ReadAll(res.Body)
	if err != nil {
		return resp, err
	}

	if res.StatusCode != wantStatus {
		return resp, fmt.Errorf("%s %s: want %d, got %d: %s", method, url, wantStatus, res.StatusCode, b)
	}

	if err := json.Unmarshal(b, &resp); err != nil {
		return resp, err
	}

	return resp, nil
}
