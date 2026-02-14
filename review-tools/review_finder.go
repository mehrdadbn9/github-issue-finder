package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/go-github/v58/github"
	"golang.org/x/oauth2"
)

func main() {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN environment variable required")
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
		{"grafana", "grafana"},
	}

	for _, project := range projects {
		fmt.Printf("\n=== %s/%s ===\n", project.Owner, project.Repo)

		prs, _, err := client.PullRequests.List(ctx, project.Owner, project.Repo, &github.PullRequestListOptions{
			State: "open",
			ListOptions: github.ListOptions{
				PerPage: 10,
			},
		})
		if err != nil {
			log.Printf("Error fetching PRs for %s/%s: %v", project.Owner, project.Repo, err)
			continue
		}

		for _, pr := range prs {
			age := time.Since(pr.GetCreatedAt().Time)
			comments := pr.GetComments()
			additions := pr.GetAdditions()
			deletions := pr.GetDeletions()

			fmt.Printf("\nPR #%d: %s\n", pr.GetNumber(), pr.GetTitle())
			fmt.Printf("  URL: %s\n", pr.GetHTMLURL())
			fmt.Printf("  Author: %s\n", pr.GetUser().GetLogin())
			fmt.Printf("  Age: %s\n", age.Round(time.Hour))
			fmt.Printf("  Comments: %d\n", comments)
			fmt.Printf("  Changes: +%d/-%d\n", additions, deletions)
			fmt.Printf("  Labels: ")
			for _, label := range pr.Labels {
				fmt.Printf("%s ", label.GetName())
			}
			fmt.Println()
		}
	}
}
