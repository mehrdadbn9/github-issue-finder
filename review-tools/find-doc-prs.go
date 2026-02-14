package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
	"golang.org/x/oauth2"
)

func main() {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN required")
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	projects := []struct {
		Owner string
		Repo  string
	}{
		{"VictoriaMetrics", "VictoriaMetrics"},
		{"kubernetes", "kubernetes"},
		{"prometheus", "prometheus"},
	}

	fmt.Println("=== DOCUMENTATION PRs Perfect for DevOps Engineers ===\n")

	for _, project := range projects {
		prs, _, err := client.PullRequests.List(ctx, project.Owner, project.Repo, &github.PullRequestListOptions{
			State: "open",
			ListOptions: github.ListOptions{PerPage: 20},
		})
		if err != nil {
			continue
		}

		for _, pr := range prs {
			title := strings.ToLower(pr.GetTitle())
			files, _, _ := client.PullRequests.ListFiles(ctx, project.Owner, project.Repo, pr.GetNumber(), &github.ListOptions{})
			
			hasDocs := false
			hasGo := false
			for _, f := range files {
				if strings.HasSuffix(f.GetFilename(), ".md") || strings.Contains(f.GetFilename(), "docs/") {
					hasDocs = true
				}
				if strings.HasSuffix(f.GetFilename(), ".go") {
					hasGo = true
				}
			}

			if hasDocs && !hasGo || strings.Contains(title, "doc") || strings.Contains(title, "readme") {
				fmt.Printf("📚 %s/%s #%d: %s\n", project.Owner, project.Repo, pr.GetNumber(), pr.GetTitle())
				fmt.Printf("   URL: %s\n", pr.GetHTMLURL())
				fmt.Printf("   Changed files: %d\n", pr.GetChangedFiles())
				
				age := time.Since(pr.GetCreatedAt().Time)
				fmt.Printf("   Age: %v\n", age.Round(time.Hour))
				fmt.Printf("   Comments: %d\n\n", pr.GetComments())
			}
		}
	}
}
