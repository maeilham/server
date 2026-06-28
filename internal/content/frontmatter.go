package content

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

type Frontmatter struct {
	Title   string   `yaml:"title"`
	Preview string   `yaml:"preview"`
	Tags    []string `yaml:"tags"`
}

type ParsedContent struct {
	Frontmatter Frontmatter
	Body        string
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\s*\r?\n(.*?)\r?\n---\s*\r?\n(.*)\z`)

func Parse(raw []byte) (*ParsedContent, error) {
	m := frontmatterRe.FindSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("frontmatter not found (expected '---' delimiters at the top)")
	}

	var fm Frontmatter
	if err := yaml.Unmarshal(m[1], &fm); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if err := validate(&fm); err != nil {
		return nil, err
	}

	body := bytes.TrimSpace(m[2])
	return &ParsedContent{
		Frontmatter: fm,
		Body:        string(body),
	}, nil
}

func validate(fm *Frontmatter) error {
	if fm.Title == "" {
		return fmt.Errorf("frontmatter.title is required")
	}
	if fm.Preview == "" {
		return fmt.Errorf("frontmatter.preview is required")
	}
	return nil
}
