package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const linearEndpoint = "https://api.linear.app/graphql"

// GraphQL request/response shapes

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type assignee struct {
	Name string `json:"name"`
}

type issueNode struct {
	Title       string    `json:"title"`
	CompletedAt time.Time `json:"completedAt"`
	Assignee    *assignee `json:"assignee"`
}

type issuesConnection struct {
	Nodes    []issueNode `json:"nodes"`
	PageInfo pageInfo    `json:"pageInfo"`
}

type gqlData struct {
	Issues issuesConnection `json:"issues"`
}

type gqlResponse struct {
	Data   gqlData `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Output shape matching issues.json
type outputIssue struct {
	Engineer    string `json:"engineer"`
	Title       string `json:"title"`
	CompletedAt string `json:"completed_at"`
}

const issuesQuery = `
query FetchCompletedIssues($after: String) {
  issues(
    first: 250
    after: $after
    filter: {
      state: { type: { eq: "completed" } }
      completedAt: { null: false }
      assignee: { null: false }
    }
    orderBy: updatedAt
  ) {
    nodes {
      title
      completedAt
      assignee {
        name
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
`

func fetchPage(client *http.Client, apiKey, cursor string) (*gqlResponse, error) {
	vars := map[string]any{}
	if cursor != "" {
		vars["after"] = cursor
	}

	body, err := json.Marshal(gqlRequest{Query: issuesQuery, Variables: vars})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, linearEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}

	var gqlResp gqlResponse
	if err := json.Unmarshal(raw, &gqlResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	return &gqlResp, nil
}

func main() {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: LINEAR_API_KEY environment variable is not set")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	enc := json.NewEncoder(os.Stdout)

	var cursor string
	totalFetched := 0

	for {
		resp, err := fetchPage(client, apiKey, cursor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		for _, node := range resp.Data.Issues.Nodes {
			if node.Assignee == nil || node.CompletedAt.IsZero() {
				continue
			}
			out := outputIssue{
				Engineer:    node.Assignee.Name,
				Title:       node.Title,
				CompletedAt: node.CompletedAt.UTC().Format(time.RFC3339),
			}
			if err := enc.Encode(out); err != nil {
				fmt.Fprintf(os.Stderr, "error encoding output: %v\n", err)
				os.Exit(1)
			}
		}

		totalFetched += len(resp.Data.Issues.Nodes)
		fmt.Fprintf(os.Stderr, "fetched %d issues...\n", totalFetched)

		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Issues.PageInfo.EndCursor
	}

	fmt.Fprintf(os.Stderr, "done. total issues: %d\n", totalFetched)
}
