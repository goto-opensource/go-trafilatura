// This file is part of go-trafilatura, Go package for extracting readable
// content, comments and metadata from a web page. Source available in
// <https://github.com/markusmobius/go-trafilatura>.
//
// Copyright (C) 2021 Markus Mobius
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"net/http"
	nurl "net/url"
	"time"

	"github.com/markusmobius/go-trafilatura"
	"github.com/spf13/cobra"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sync/semaphore"
)

func crawlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crawl [flags] [source]",
		Short: "crawl a fixed number of pages within a website starting from the given URL",
		Long: "Crawl a fixed number of pages within a website starting from the given URL.\n" +
			"Recursively follows links up to a specified depth, downloading pages in parallel.\n" +
			"Options allow you to set the maximum number of URLs, recursion depth, delay between requests, and restrict crawling to the same domain.\n" +
			"Useful for collecting URLs or content from a site for further processing.",
		Args: cobra.ExactArgs(1),
		Run:  crawlCmdHandler,
	}

	flags := cmd.Flags()
	flags.Int("parallel", 10, "number of concurrent download at a time (default 10)")
	flags.Duration("delay", time.Millisecond*250, "delay between each url download (default 250ms)")

	flags.Int("max-urls", 0, "maximum number of URLs to download (default 0)")
	flags.Int("max-depth", 2, "maximum recursion depth (default 2)")
	flags.Bool("no-same-domain", false, "restrict recursion to same domain")

	return cmd
}

func crawlCmdHandler(cmd *cobra.Command, args []string) {
	// Parse arguments
	flags := cmd.Flags()
	delay, _ := flags.GetDuration("delay")
	nThread, _ := flags.GetInt("parallel")
	nURL, _ := flags.GetInt("max-urls")
	nDepth, _ := flags.GetInt("max-depth")
	userAgent, _ := cmd.Flags().GetString("user-agent")
	notSameDomain, _ := flags.GetBool("no-same-domain")

	log.Info().Int("nURL", nURL).Int("nDepth", nDepth).Int("nThread", nThread).Bool("notSameDomain", notSameDomain).Msgf("Crawling URL")

	opts := createExtractorOptions(cmd)
	opts.IncludeLinksOnly = true
	opts.Focus = trafilatura.FavorPrecision
	opts.EnableFallback = false

	startURL, err := nurl.Parse(args[0])
	if err != nil {
		log.Fatal().Msgf("invalid start URL: %v", err)
	}

	urls, err := (&crawler{
		userAgent:      userAgent,
		httpClient:     createHttpClient(cmd),
		extractOptions: opts,
		semaphore:      semaphore.NewWeighted(int64(nThread)),
		delay:          delay,
		cancelOnError:  false,
		sameDomain:     !notSameDomain,
		maxURLs:        nURL,
		maxDepth:       nDepth,
	}).extractURLs(context.Background(), *startURL)

	if err != nil {
		log.Fatal().Msgf("process failed: %v", err)
	}
	log.Info().Msgf("Crawled %d URLs", len(urls))
	for _, url := range urls {
		log.Info().Msgf("%s", url.String())
	}
}

type crawler struct {
	extractOptions trafilatura.Options
	semaphore      *semaphore.Weighted
	httpClient     *http.Client
	userAgent      string
	delay          time.Duration
	cancelOnError  bool
	sameDomain     bool
	maxURLs        int
	maxDepth       int
	urls           []nurl.URL
}

func extractLinksFromURL(httpClient *http.Client, userAgent string, source nurl.URL, opts trafilatura.Options) ([]nurl.URL, error) {
	if !isValidURL(source.String()) {
		return nil, fmt.Errorf("invalid URL: %s", source.String())
	}
	result, err := processURL(httpClient, userAgent, &source, opts)
	if err != nil {
		return nil, err
	}
	log.Info().Str("url", source.String()).Int("size", len(result.URLs)).Msgf("Found URL")

	return result.URLs, nil
}

// normalizeURLString returns the URL as a string, removing a trailing slash unless the path is just "/"
func normalizeURLString(u nurl.URL) string {
	s := u.String()
	if u.Path == "/" || u.Path == "" {
		return s
	}
	if len(u.Path) > 1 && u.Path[len(u.Path)-1] == '/' {
		// Remove trailing slash from path
		copyU := u
		copyU.Path = u.Path[:len(u.Path)-1]
		return copyU.String()
	}
	return s
}

func (c *crawler) extractURLs(context context.Context, source nurl.URL) ([]nurl.URL, error) {
	maxURLs := c.maxURLs
	maxDepth := c.maxDepth

	visited := make(map[string]bool)
	var result []nurl.URL
	queue := []struct {
		url   nurl.URL
		depth int
	}{{source, 0}}

	sourceTLD, err := getTopLevelDomain(source)
	if err != nil {
		return nil, err
	}

	for len(queue) > 0 && (maxURLs == 0 || len(result) < maxURLs) {
		// Group all items at the current depth
		currentDepth := queue[0].depth
		var currentLevel []struct {
			url   nurl.URL
			depth int
		}
		for len(queue) > 0 && queue[0].depth == currentDepth {
			currentLevel = append(currentLevel, queue[0])
			queue = queue[1:]
		}

		// Channel to collect children from all goroutines
		childrenCh := make(chan []nurl.URL, len(currentLevel))
		goroutinesStarted := 0

		// Process all URLs at this depth in parallel
		for _, item := range currentLevel {
			norm := normalizeURLString(item.url)
			if visited[norm] || (maxDepth > 0 && item.depth > maxDepth) {
				continue
			}
			visited[norm] = true
			result = append(result, item.url)
			if maxURLs > 0 && len(result) >= maxURLs {
				break
			}

			// Only extract links if we haven't reached maxDepth
			if maxDepth == 0 || item.depth < maxDepth {
				// Acquire semaphore for parallelism
				if err := c.semaphore.Acquire(context, 1); err != nil {
					childrenCh <- nil
					continue
				}
				goroutinesStarted++

				go func(item struct {
					url   nurl.URL
					depth int
				}) {
					defer c.semaphore.Release(1)
					children, err := extractLinksFromURL(c.httpClient, c.userAgent, item.url, c.extractOptions)
					if err != nil {
						log.Warn().Msgf("failed to extract links: %v", err)
						childrenCh <- nil
						return
					}
					if c.delay > 0 {
						time.Sleep(c.delay)
					}
					childrenCh <- children
				}(item)
			}
		}

		// Collect all children from this depth
		var allChildren []nurl.URL
		for i := 0; i < goroutinesStarted; i++ {
			children := <-childrenCh
			for _, child := range children {
				norm := normalizeURLString(child)
				if visited[norm] {
					continue
				}
				if c.sameDomain {
					childTLD, err := getTopLevelDomain(child)
					if err != nil {
						continue
					}
					if childTLD != sourceTLD {
						log.Debug().Msgf("Skipping URL %s because it's a different TLD", child.String())
						continue
					}
				}
				allChildren = append(allChildren, child)
			}
		}

		// Enqueue deduplicated children for next depth
		for _, child := range allChildren {
			norm := normalizeURLString(child)
			if !visited[norm] {
				queue = append(queue, struct {
					url   nurl.URL
					depth int
				}{child, currentDepth + 1})
			}
		}
	}

	return result, nil
}

func getTopLevelDomain(domain nurl.URL) (string, error) {
	topLevelDomain, err := publicsuffix.EffectiveTLDPlusOne(domain.Hostname())
	if err != nil {
		return "", err
	}
	return topLevelDomain, nil
}
