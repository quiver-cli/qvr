package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/spf13/cobra"
)

var (
	searchGithub bool
	searchFull   bool
	searchTags   []string
	searchAuthor string
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for skills across registries",
	Long: `Search skills by name, description, or metadata. Substring matches on
name and description, plus optional --tag and --author hard filters.
At least one of <query>, --tag, or --author must be supplied.

Use --github to search GitHub for repositories with the agent-skills topic.
Use --full to print untruncated descriptions.`,
	Args: cobra.ArbitraryArgs,
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().BoolVar(&searchGithub, "github", false,
		"search GitHub for repos with agent-skills topic")
	searchCmd.Flags().BoolVar(&searchFull, "full", false,
		"print full descriptions without truncation")
	searchCmd.Flags().StringSliceVar(&searchTags, "tag", nil,
		"filter by metadata.tags (repeatable; entries must match all)")
	searchCmd.Flags().StringVar(&searchAuthor, "author", "",
		"filter by metadata.author")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	if searchGithub {
		if query == "" {
			return fmt.Errorf("--github requires a query")
		}
		if len(searchTags) > 0 || searchAuthor != "" {
			return fmt.Errorf("--github cannot be combined with --tag or --author (those filter local registry indexes)")
		}
		return runGitHubSearch(cmd, query)
	}

	if query == "" && len(searchTags) == 0 && searchAuthor == "" {
		return fmt.Errorf("provide a query, --tag, or --author")
	}

	return runLocalSearch(cmd, registry.SearchFilter{
		Query:  query,
		Tags:   searchTags,
		Author: searchAuthor,
	})
}

func runLocalSearch(cmd *cobra.Command, filter registry.SearchFilter) error {
	mgr := registry.NewManager(git.NewGoGitClient())

	results, err := mgr.SearchWithFilter(filter)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	if printer.Format == output.FormatJSON {
		// Coerce nil to [] so consumers can always `jq 'length'` without
		// special-casing the empty-result branch.
		if results == nil {
			results = []registry.SearchResult{}
		}
		return printer.JSON(results)
	}

	if len(results) == 0 {
		printer.Info(fmt.Sprintf("No skills found matching %s", describeFilter(filter)))
		return nil
	}

	headers := []string{"NAME", "REGISTRY", "DESCRIPTION"}
	var rows [][]string
	for _, r := range results {
		rows = append(rows, []string{r.Name, r.Registry, output.TruncDesc(r.Description, searchFull)})
	}
	printer.Table(headers, rows)
	return nil
}

// describeFilter renders a human-readable summary of what was searched for,
// used in the "no results" message so users see why they got an empty list.
func describeFilter(f registry.SearchFilter) string {
	parts := []string{}
	if f.Query != "" {
		parts = append(parts, fmt.Sprintf("query=%q", f.Query))
	}
	if len(f.Tags) > 0 {
		parts = append(parts, fmt.Sprintf("tags=%s", strings.Join(f.Tags, ",")))
	}
	if f.Author != "" {
		parts = append(parts, fmt.Sprintf("author=%s", f.Author))
	}
	if len(parts) == 0 {
		return "(no filter)"
	}
	return strings.Join(parts, " ")
}

// gitHubSearchResult represents a GitHub repository search result.
type gitHubSearchResult struct {
	Name        string   `json:"name"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	HTMLURL     string   `json:"html_url"`
	CloneURL    string   `json:"clone_url"`
	Stars       int      `json:"stargazers_count"`
	Topics      []string `json:"topics"`
}

func runGitHubSearch(cmd *cobra.Command, query string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ghQuery := fmt.Sprintf("topic:agent-skills %s", query)
	apiURL := fmt.Sprintf("https://api.github.com/search/repositories?q=%s&sort=stars&per_page=20",
		url.QueryEscape(ghQuery))

	req, err := http.NewRequestWithContext(cmd.Context(), "GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if cfg.GithubToken != "" {
		req.Header.Set("Authorization", "token "+cfg.GithubToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("github search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return fmt.Errorf("github API rate limit exceeded; set a token with: qvr config set github_token <token>")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github search returned %d: %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		TotalCount int                  `json:"total_count"`
		Items      []gitHubSearchResult `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(searchResp)
	}

	if len(searchResp.Items) == 0 {
		printer.Info(fmt.Sprintf("No GitHub repositories found for %q with topic 'agent-skills'", query))
		return nil
	}

	headers := []string{"REPO", "STARS", "DESCRIPTION"}
	var rows [][]string
	for _, item := range searchResp.Items {
		rows = append(rows, []string{
			item.FullName,
			fmt.Sprintf("%d", item.Stars),
			output.TruncDesc(item.Description, searchFull),
		})
	}
	printer.Table(headers, rows)
	printer.Info(fmt.Sprintf("\nFound %d results. Add with: qvr add <repo-url>", searchResp.TotalCount))
	return nil
}
