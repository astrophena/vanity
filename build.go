//usr/bin/env go run $0 $@; exit $?

// © 2023 Ilya Mateyko. All rights reserved.
// Use of this source code is governed by the ISC
// license that can be found in the LICENSE file.

//go:build ignore

package main

import (
	"bytes"
	"embed"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const highlightTheme = "native"

var (
	//go:embed *.html
	tplFS    embed.FS
	tplFuncs = template.FuncMap{
		"contains":  strings.Contains,
		"hasOnePkg": hasOnePkg,
	}
	tpl = template.Must(template.New("vanity").Funcs(tplFuncs).ParseFS(tplFS, "*.html"))
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

	if err := build(dir, token); err != nil {
		log.Fatal(err)
	}
}

const userReposURL = "https://api.github.com/users/astrophena/repos"

func build(dir, token string) error {
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

	// Filter only public Go modules.
	var repos []*repo
	for _, repo := range allRepos {
		if repo.Private || repo.Fork || repo.Name == "vanity" {
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

	tmpdir, err := os.MkdirTemp("", "vanity")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)

	for _, repo := range repos {
		if !strings.HasSuffix(repo.Description, ".") {
			repo.Description += "."
		}

		if err := exec.Command("git", "clone", "--depth=1", repo.CloneURL, filepath.Join(tmpdir, repo.Name)).Run(); err != nil {
			return err
		}
		repo.Dir = filepath.Join(tmpdir, repo.Name)

		var obuf, errbuf bytes.Buffer
		list := exec.Command("go", "list", "-json", "./...")
		list.Dir = repo.Dir
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
			p.Repo = repo
			repo.Pkgs = append(repo.Pkgs, p)
		}
	}

	// Build index page.
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "index", repos); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Build repo and package pages.
	for _, repo := range repos {
		buf.Reset()

		git := exec.Command("git", "rev-parse", "--short", "HEAD")
		git.Dir = repo.Dir
		commitb, err := git.Output()
		if err != nil {
			return err
		}
		commitn := string(commitb)
		repo.Commit = strings.TrimSuffix(commitn, "\n")

		if err := repo.generateDoc(); err != nil {
			return err
		}

		var pkgbuf bytes.Buffer
		for _, pkg := range repo.Pkgs {
			pkg.BasePath = strings.TrimPrefix(pkg.ImportPath, "go.astrophena.name/")
			pkg.SrcPath = strings.TrimPrefix(pkg.BasePath, repo.Name+"/")
			// If we have a single source file, it's better directly link to it.
			if len(pkg.GoFiles) == 1 {
				pkg.SrcPath = filepath.Join(pkg.SrcPath, pkg.GoFiles[0])
			}
			if pkg.BasePath == repo.Name || strings.Contains(pkg.BasePath, "internal") {
				continue
			}
			pkgbuf.Reset()
			if err := tpl.ExecuteTemplate(&pkgbuf, "pkg", pkg); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, pkg.BasePath)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, pkg.BasePath+".html"), pkgbuf.Bytes(), 0o644); err != nil {
				return err
			}
		}

		if err := tpl.ExecuteTemplate(&buf, "import", repo); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, repo.Name+".html"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}

	hcss, err := exec.Command("doc2go", "-highlight", highlightTheme, "-highlight-print-css").Output()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "doc2go.css"), hcss, 0o644); err != nil {
		return err
	}

	return nil
}

type repo struct {
	// From GitHub API:
	Name        string `json:"name"`
	URL         string `json:"url"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	CloneURL    string `json:"clone_url"`
	Fork        bool   `json:"fork"`
	// Obtained by 'git rev-parse --short HEAD'
	Commit string
	// For use with doc2go
	Dir string
	// Go packages that this repo contains
	Pkgs []*pkg
}

type pkg struct {
	// bits of 'go list -json' that we need.
	Name       string   // package name
	ImportPath string   // import path of package in dir
	Doc        string   // package documentation string
	GoFiles    []string // .go source files
	Imports    []string // import paths used by this package

	FullDoc template.HTML // generated by doc2go

	BasePath string
	SrcPath  string

	Repo *repo
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

func (r *repo) generateDoc() error {
	tmpdir, err := os.MkdirTemp("", "vanity-doc2go")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)

	doc2go := exec.Command(
		"go", "run", "go.abhg.dev/doc2go@latest",
		"-highlight",
		"classes:"+highlightTheme,
		"-embed", "-out", tmpdir,
		"./...",
	)
	doc2go.Dir = r.Dir
	if err := doc2go.Run(); err != nil {
		return err
	}

	for _, pkg := range r.Pkgs {
		docfile := filepath.Join(tmpdir, pkg.ImportPath, "index.html")
		if _, err := os.Stat(docfile); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}

		fullDoc, err := os.ReadFile(docfile)
		if err != nil {
			return err
		}
		pkg.FullDoc = template.HTML(fullDoc)
	}

	return nil
}

func hasOnePkg(r *repo) bool {
	if len(r.Pkgs) != 1 {
		return false
	}

	return r.Pkgs[0].ImportPath == "go.astrophena.name/"+r.Name
}
