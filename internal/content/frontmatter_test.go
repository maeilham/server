package content

import (
	"strings"
	"testing"
)

func TestParse_Valid(t *testing.T) {
	raw := []byte(`---
title: "스케일업과 스케일아웃의 차이는?"
preview: "서버 한계를 마주했을 때 떠올리는 두 가지 선택지."
tags: [infra, scaling]
---

## 질문
본문 내용
`)
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Frontmatter.Title != "스케일업과 스케일아웃의 차이는?" {
		t.Errorf("title mismatch: %q", p.Frontmatter.Title)
	}
	if p.Frontmatter.Preview != "서버 한계를 마주했을 때 떠올리는 두 가지 선택지." {
		t.Errorf("preview mismatch: %q", p.Frontmatter.Preview)
	}
	if len(p.Frontmatter.Tags) != 2 || p.Frontmatter.Tags[0] != "infra" || p.Frontmatter.Tags[1] != "scaling" {
		t.Errorf("tags mismatch: %v", p.Frontmatter.Tags)
	}
	if p.Frontmatter.Source != nil {
		t.Errorf("expected nil source, got %+v", p.Frontmatter.Source)
	}
	if !strings.HasPrefix(p.Body, "## 질문") {
		t.Errorf("body should start with ## 질문, got %q", p.Body)
	}
	if p.BodyHash == "" || len(p.BodyHash) != 64 {
		t.Errorf("body hash should be 64-char hex, got %q", p.BodyHash)
	}
}

func TestParse_WithSource(t *testing.T) {
	raw := []byte(`---
title: "T"
preview: "P"
source:
  url: "https://example.com/x"
  author: "anon"
---
body`)
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Frontmatter.Source == nil {
		t.Fatal("source should not be nil")
	}
	if p.Frontmatter.Source.URL != "https://example.com/x" {
		t.Errorf("source.url mismatch: %q", p.Frontmatter.Source.URL)
	}
	if p.Frontmatter.Source.Author != "anon" {
		t.Errorf("source.author mismatch: %q", p.Frontmatter.Source.Author)
	}
}

func TestParse_TrimsBody(t *testing.T) {
	raw := []byte(`---
title: "T"
preview: "P"
---



body has leading and trailing whitespace


`)
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Body != "body has leading and trailing whitespace" {
		t.Errorf("body not trimmed: %q", p.Body)
	}
}

func TestParse_CRLF(t *testing.T) {
	raw := []byte("---\r\ntitle: \"T\"\r\npreview: \"P\"\r\n---\r\n\r\nbody\r\n")
	if _, err := Parse(raw); err != nil {
		t.Fatalf("CRLF should be supported: %v", err)
	}
}

func TestParse_HashIsDeterministic(t *testing.T) {
	raw := []byte(`---
title: "T"
preview: "P"
---
same body`)
	p1, _ := Parse(raw)
	p2, _ := Parse(raw)
	if p1.BodyHash != p2.BodyHash {
		t.Errorf("hash should be deterministic: %s vs %s", p1.BodyHash, p2.BodyHash)
	}
}

func TestParse_HashChangesWithBody(t *testing.T) {
	a, _ := Parse([]byte(`---
title: "T"
preview: "P"
---
A`))
	b, _ := Parse([]byte(`---
title: "T"
preview: "P"
---
B`))
	if a.BodyHash == b.BodyHash {
		t.Errorf("hash should differ between distinct bodies")
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "no delimiters",
			raw:     "## just markdown",
			wantErr: "frontmatter not found",
		},
		{
			name: "broken yaml",
			raw: `---
title: "T
preview: "P"
---
body`,
			wantErr: "yaml",
		},
		{
			name: "missing title",
			raw: `---
preview: "P"
---
body`,
			wantErr: "title is required",
		},
		{
			name: "missing preview",
			raw: `---
title: "T"
---
body`,
			wantErr: "preview is required",
		},
		{
			name: "empty title",
			raw: `---
title: ""
preview: "P"
---
body`,
			wantErr: "title is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
