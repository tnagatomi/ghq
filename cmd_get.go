package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/Songmu/gitconfig"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v2"
	"github.com/x-motemen/ghq/cmdutil"
	"github.com/x-motemen/ghq/logger"
	"golang.org/x/sync/errgroup"
)

func doGet(c *cli.Context) error {
	var (
		args     = c.Args().Slice()
		andLook  = c.Bool("look")
		parallel = c.Bool("parallel")
	)
	g := &getter{
		update:    c.Bool("update"),
		shallow:   c.Bool("shallow"),
		ssh:       c.Bool("p"),
		vcs:       c.String("vcs"),
		silent:    c.Bool("silent"),
		branch:    c.String("branch"),
		recursive: !c.Bool("no-recursive"),
		bare:      c.Bool("bare"),
	}
	if parallel {
		// force silent in parallel import
		g.silent = true
	}

	protocol, err := gitconfig.Get("ghq.protocol")
	if err != nil && !gitconfig.IsNotFound(err) {
		return err
	}
	if protocol == "ssh" {
		g.ssh = true
	}

	var (
		firstArg string // Look at the first repo only, if there are more than one
		argCnt   int
		getInfo  getInfo // For fetching and looking a single repo
		scr      scanner
	)
	if len(args) > 0 {
		scr = &sliceScanner{slice: args}
	} else {
		fd := os.Stdin.Fd()
		if isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd) {
			return fmt.Errorf("no target args specified. see `ghq get -h` for more details")
		}
		scr = bufio.NewScanner(os.Stdin)
	}

	eg := &errgroup.Group{}
	sem := make(chan struct{}, 6)
	for scr.Scan() {
		target := scr.Text()
		if firstArg == "" {
			firstArg = target
		}
		argCnt += 1
		if parallel {
			sem <- struct{}{}
			eg.Go(func() error {
				defer func() { <-sem }()
				if getInfo, err = g.get(target); err != nil {
					logger.Logf("error", "failed to get %q: %s", target, err)
				}
				return nil
			})
		} else {
			if getInfo, err = g.get(target); err != nil {
				return fmt.Errorf("failed to get %q: %w", target, err)
			}
		}
	}
	if err = scr.Err(); err != nil {
		return fmt.Errorf("error occurred while reading input: %w", err)
	}
	if err = eg.Wait(); err != nil {
		return err
	}
	if andLook {
		if argCnt > 1 && firstArg != "" {
			return look(firstArg, g.bare)
		}
		if argCnt == 1 && getInfo.localRepository != nil {
			return lookByLocalRepository(getInfo.localRepository)
		}
	}
	return nil
}

type sliceScanner struct {
	slice []string
	index int
}

func (s *sliceScanner) Scan() bool {
	s.index++
	return s.index <= len(s.slice)
}

func (s *sliceScanner) Text() string {
	return s.slice[s.index-1]
}

func (s *sliceScanner) Err() error {
	return nil
}

type scanner interface {
	Scan() bool
	Text() string
	Err() error
}

func detectShell() string {
	shell := os.Getenv("SHELL")
	if shell != "" {
		return shell
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("COMSPEC")
	}
	return "/bin/sh"
}

func look(name string, bare bool) error {
	var (
		reposFound []*LocalRepository
		mu         sync.Mutex
	)
	if err := walkAllLocalRepositories(func(repo *LocalRepository) {
		if repo.Matches(name) {
			mu.Lock()
			reposFound = append(reposFound, repo)
			mu.Unlock()
		}
	}); err != nil {
		return err
	}

	if len(reposFound) == 0 {
		if url, err := newURL(name, false, false); err == nil {
			repo, err := LocalRepositoryFromURL(url, bare)
			if err != nil {
				return err
			}
			_, err = os.Stat(repo.FullPath)

			// if the directory exists
			if err == nil {
				reposFound = append(reposFound, repo)
			}
		}
	}

	switch len(reposFound) {
	case 0:
		return fmt.Errorf("no repository found")
	case 1:
		return lookByLocalRepository(reposFound[0])
	default:
		b := &strings.Builder{}
		b.WriteString("More than one repositories are found; Try more precise name\n")
		for _, repo := range reposFound {
			b.WriteString(fmt.Sprintf("       - %s\n", strings.Join(repo.PathParts, "/")))
		}
		return errors.New(b.String())
	}
}

func lookByLocalRepository(repo *LocalRepository) error {
	cmd := exec.Command(detectShell())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = repo.FullPath
	cmd.Env = append(os.Environ(), "GHQ_LOOK="+filepath.ToSlash(repo.RelPath))
	return cmdutil.RunCommand(cmd, true)
}
