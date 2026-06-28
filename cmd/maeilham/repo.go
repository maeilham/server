package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/maeilham/server/internal/store"
)

type RepoCmd struct {
	Add        RepoAddCmd        `cmd:"" help:"repo 등록/수정"`
	List       RepoListCmd       `cmd:"" help:"등록된 repo 목록"`
	Deactivate RepoDeactivateCmd `cmd:"" help:"repo 비활성화"`
}

type RepoAddCmd struct {
	Slug string `required:"" help:"레포 슬러그 (예: be-interview)"`
	URL  string `required:"" help:"GitHub URL"`
	Name string `required:"" help:"표시 이름"`
	Desc string `help:"설명 (선택)"`
}

func (c *RepoAddCmd) Run(ctx context.Context, d *deps) error {
	if err := d.repoStore.Upsert(ctx, &store.Repo{
		Slug:        c.Slug,
		GitHubURL:   c.URL,
		DisplayName: c.Name,
		Description: c.Desc,
	}); err != nil {
		return err
	}
	fmt.Printf("✓ repo 추가됨: %s\n", c.Slug)
	return nil
}

type RepoListCmd struct{}

func (c *RepoListCmd) Run(ctx context.Context, d *deps) error {
	repos, err := d.repoStore.ListAll(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-25s %-10s %s\n", "SLUG", "ACTIVE", "URL")
	fmt.Println(strings.Repeat("-", 70))
	for _, r := range repos {
		status := "✓"
		if !r.Active {
			status = "✗"
		}
		fmt.Printf("%-25s %-10s %s  (%s)\n", r.Slug, status, r.GitHubURL, r.DisplayName)
	}
	return nil
}

type RepoDeactivateCmd struct {
	Slug string `required:"" help:"레포 슬러그"`
}

func (c *RepoDeactivateCmd) Run(ctx context.Context, d *deps) error {
	if err := d.repoStore.Deactivate(ctx, c.Slug); err != nil {
		return err
	}
	fmt.Printf("✓ repo 비활성화됨: %s\n", c.Slug)
	return nil
}
